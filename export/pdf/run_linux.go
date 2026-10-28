//go:build linux

package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	r "idp.local/reporting/reporting"
)

// fileID identifies a file's content without reading it: replacing the file changes the inode
// and writing to it changes the modification and status-change times.
type fileID struct {
	dev, ino     uint64
	mode         uint32
	size         int64
	mtime, ctime syscall.Timespec
}

func toID(st *syscall.Stat_t) fileID {
	return fileID{dev: uint64(st.Dev), ino: st.Ino, mode: st.Mode, size: st.Size, mtime: st.Mtim, ctime: st.Ctim}
}

// identity refuses symbolic links and files that anyone but root and this user may write.
func identity(path string) (fileID, error) {
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		return fileID{}, fmt.Errorf("pdf: %s: %w", filepath.Base(path), err)
	}
	if st.Mode&syscall.S_IFMT == syscall.S_IFLNK || !ownWrites(&st) {
		return fileID{}, fmt.Errorf("pdf: %s is a symbolic link or writable by others than root and this user", filepath.Base(path))
	}
	return toID(&st), nil
}

// openIdentity identifies an open regular file that only root and this user may write.
func openIdentity(f *os.File) (fileID, error) {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return fileID{}, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || !ownWrites(&st) {
		return fileID{}, errors.New("pdf: the Typst binary is not a regular file or is writable by others than root and this user")
	}
	return toID(&st), nil
}

// ownWrites: the file is owned by root or this user and nobody else may write it.
func ownWrites(st *syscall.Stat_t) bool {
	return (st.Uid == 0 || st.Uid == uint32(os.Getuid())) && st.Mode&0o022 == 0
}

// staleAfter is when a render directory is certainly left over: a render takes less than
// reporting.WorkTimeout.
const staleAfter = 10 * time.Minute

// checkHost refuses hosts where Typst's core dumps could escape, and font or work directories
// that others could replace, and removes render directories that a stopped process left behind
// (they hold row values).
func checkHost(c Config) error {
	if err := coreDumpsContained(); err != nil {
		return err
	}
	for _, dir := range []string{c.FontDir, c.WorkDir} {
		if err := trustedAncestors(filepath.Join(dir, "x")); err != nil {
			return err
		}
	}
	sweep(c.WorkDir, time.Now())
	return nil
}

// coreDumpsContained: Typst's core dumps would hold the data. A core limit of 1 stops them,
// also when kernel.core_pattern pipes them to a crash collector (systemd-coredump, apport,
// WSL), which ignores a limit of 0. When this process may not raise its limit to 1, Typst gets
// 0, which is enough only when the kernel writes dumps to files.
func coreDumpsContained() error {
	var l syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &l); err != nil {
		return err
	}
	if l.Max >= 1 {
		return nil
	}
	pattern, err := os.ReadFile("/proc/sys/kernel/core_pattern")
	if err != nil {
		return fmt.Errorf("pdf: read kernel.core_pattern: %w", err)
	}
	if strings.HasPrefix(string(pattern), "|") {
		return errors.New("pdf: the hard core limit is 0 and kernel.core_pattern pipes core dumps to a crash collector, which ignores that limit; allow a core limit of 1")
	}
	return nil
}

// trustedAncestors refuses a path whose directories others could rename or replace: each must be
// owned by root or this user, and writable by others only with the sticky bit set.
func trustedAncestors(path string) error {
	uid := uint32(os.Getuid())
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		var st syscall.Stat_t
		if err := syscall.Lstat(dir, &st); err != nil {
			return fmt.Errorf("pdf: %s: %w", dir, err)
		}
		if st.Mode&syscall.S_IFMT != syscall.S_IFDIR || (st.Uid != 0 && st.Uid != uid) || (st.Mode&0o022 != 0 && st.Mode&syscall.S_ISVTX == 0) {
			return fmt.Errorf("pdf: %s is not a directory that only root or this user can change", dir)
		}
		if dir == filepath.Dir(dir) {
			return nil
		}
	}
}

// sweep removes this user's render directories in workDir last changed before staleAfter.
func sweep(workDir string, now time.Time) {
	entries, _ := os.ReadDir(workDir)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "reporting-pdf-") {
			continue
		}
		p := filepath.Join(workDir, e.Name())
		var st syscall.Stat_t
		if syscall.Lstat(p, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFDIR || st.Uid != uint32(os.Getuid()) {
			continue
		}
		if now.Sub(time.Unix(st.Mtim.Unix())) > staleAfter {
			_ = os.RemoveAll(p)
		}
	}
}

// render compiles dir/main.typ to a PDF/A-2b document.
func (w *Writer) render(ctx context.Context, dir string, created time.Time) ([]byte, error) {
	packages := filepath.Join(dir, "packages")
	out := filepath.Join(dir, "out.pdf")
	_, err := w.run(ctx, dir, "compile",
		"--root", dir,
		"--font-path", w.cfg.FontDir, "--ignore-system-fonts", "--ignore-embedded-fonts",
		"--package-path", packages, "--package-cache-path", packages,
		"--creation-timestamp", strconv.FormatInt(created.Unix(), 10),
		"--pdf-standard", "a-2b",
		"--jobs", strconv.Itoa(w.cfg.Jobs),
		"--diagnostic-format", "short",
		filepath.Join(dir, "main.typ"), out)
	if err != nil {
		return nil, err
	}
	return readBounded(out)
}

// run runs Typst in dir and returns its standard output. Typst gets an empty environment, its
// own user and network namespaces (no network: it would otherwise download packages on
// import), and limits on time, memory, file size and core dumps. Output on standard error
// fails the run even when Typst succeeds: it reports problems such as an unknown font family
// only as warnings.
func (w *Writer) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	run, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	defer cancel()
	// The child executes the binary New opened and checked, as its file descriptor 3.
	cmd := exec.CommandContext(run, "/proc/self/fd/3", args...)
	cmd.ExtraFiles = []*os.File{w.exe}
	cmd.Env = []string{}
	cmd.Dir = dir
	stdout, stderr := &head{}, &head{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	uid, gid := os.Getuid(), os.Getgid()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET | syscall.CLONE_NEWIPC,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
		Pdeathsig:   syscall.SIGKILL,
	}
	// The hard core limit or kernel.core_pattern may have changed since New.
	if err := coreDumpsContained(); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pdf: start Typst in new user and network namespaces (the host may forbid unprivileged user namespaces, as Ubuntu does with kernel.apparmor_restrict_unprivileged_userns=1): %w", err)
	}
	// The limits apply from here on. Typst cannot allocate gigabytes or write the output in the
	// moment between its start and this call: it first parses arguments and loads fonts.
	if err := limit(cmd.Process.Pid, w.cfg.MemoryBytes); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("pdf: limit Typst: %w", err)
	}
	err := cmd.Wait()
	status, _ := cmd.ProcessState.Sys().(syscall.WaitStatus)
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case run.Err() != nil:
		return nil, fmt.Errorf("pdf: Typst took longer than %s: %w", w.cfg.Timeout, r.ErrBudget)
	case status.Signaled() && status.Signal() == syscall.SIGXFSZ:
		return nil, fmt.Errorf("pdf: the document exceeds the artifact limit: %w", r.ErrBudget)
	case status.Signaled() && status.Signal() == syscall.SIGABRT && bytes.Contains(stderr.b, []byte("memory allocation of")):
		return nil, fmt.Errorf("pdf: Typst ran out of its memory limit: %w", r.ErrBudget)
	case err != nil:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, &RenderError{Status: exit.ProcessState.String(), stderr: stderr.b}
		}
		return nil, err
	case len(stderr.b) > 0:
		return nil, &RenderError{Status: "warnings", stderr: stderr.b}
	}
	return stdout.b, nil
}

type rlimit64 struct{ cur, max uint64 }

// limit sets hard limits on the address space, the size of any file Typst writes (one byte
// over the artifact limit, so an oversized document stops with SIGXFSZ) and core dumps (see
// coreDumpsContained). A lower hard limit of this process, which Typst would inherit anyway,
// is kept.
func limit(pid int, memory uint64) error {
	for _, l := range []struct {
		resource int
		value    uint64
	}{{syscall.RLIMIT_AS, memory}, {syscall.RLIMIT_FSIZE, r.MaxArtifactBytes + 1}, {syscall.RLIMIT_CORE, 1}} {
		var own syscall.Rlimit
		if err := syscall.Getrlimit(l.resource, &own); err != nil {
			return err
		}
		v := rlimit64{min(l.value, uint64(own.Max)), min(l.value, uint64(own.Max))}
		if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRLIMIT64, uintptr(pid), uintptr(l.resource), uintptr(unsafe.Pointer(&v)), 0, 0, 0); errno != 0 {
			return errno
		}
	}
	return nil
}
