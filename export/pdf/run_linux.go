//go:build linux

package pdf

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// identity refuses symbolic links and files that others may write.
func identity(path string) (fileID, error) {
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		return fileID{}, fmt.Errorf("pdf: %s: %w", filepath.Base(path), err)
	}
	if st.Mode&syscall.S_IFMT == syscall.S_IFLNK || st.Mode&0o022 != 0 {
		return fileID{}, fmt.Errorf("pdf: %s is a symbolic link or writable by others", filepath.Base(path))
	}
	return fileID{dev: uint64(st.Dev), ino: st.Ino, mode: st.Mode, size: st.Size, mtime: st.Mtim, ctime: st.Ctim}, nil
}

// render runs Typst on dir/main.typ. Typst gets an empty environment, its own user and network
// namespaces (no network: it would otherwise download packages on import), an empty package
// store, only the configured fonts and limits on time, memory, output size and core dumps.
func (w *Writer) render(ctx context.Context, dir string, created time.Time) ([]byte, error) {
	run, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	defer cancel()
	out := filepath.Join(dir, "out.pdf")
	packages := filepath.Join(dir, "packages")
	cmd := exec.CommandContext(run, w.cfg.Typst, "compile",
		"--root", dir,
		"--font-path", w.cfg.FontDir, "--ignore-system-fonts", "--ignore-embedded-fonts",
		"--package-path", packages, "--package-cache-path", packages,
		"--creation-timestamp", strconv.FormatInt(created.Unix(), 10),
		"--pdf-standard", "a-2b",
		"--jobs", strconv.Itoa(w.cfg.Jobs),
		"--diagnostic-format", "short",
		filepath.Join(dir, "main.typ"), out)
	cmd.Env = []string{}
	cmd.Dir = dir
	stderr := &tail{}
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	uid, gid := os.Getuid(), os.Getgid()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET | syscall.CLONE_NEWIPC,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
		Pdeathsig:   syscall.SIGKILL,
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pdf: start Typst: %w", err)
	}
	// The limits apply from here on. Typst cannot allocate gigabytes or write the output in the
	// moment between its start and this call: it first parses arguments and loads fonts.
	if err := limit(cmd.Process.Pid, w.cfg.MemoryBytes); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("pdf: limit Typst: %w", err)
	}
	err := cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case run.Err() != nil:
		return nil, fmt.Errorf("pdf: Typst took longer than %s: %w", w.cfg.Timeout, r.ErrBudget)
	case err != nil:
		if st, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && st.Signaled() && st.Signal() == syscall.SIGXFSZ {
			return nil, fmt.Errorf("pdf: the document exceeds the artifact limit: %w", r.ErrBudget)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, &RenderError{Status: exit.ProcessState.String(), stderr: stderr.b}
		}
		return nil, err
	}
	return readBounded(out)
}

type rlimit64 struct{ cur, max uint64 }

// limit sets hard limits on the address space, the size of any file Typst writes (one byte
// over the artifact limit, so an oversized document stops with SIGXFSZ) and core dumps, which
// would hold the data. A core limit of 0 does not stop a dump piped to a crash collector
// (kernel.core_pattern starting with "|", as with systemd-coredump, apport or WSL); a limit of
// 1 does, and is also below the smallest core file the kernel writes.
func limit(pid int, memory uint64) error {
	for _, l := range []struct {
		resource int
		value    uint64
	}{{syscall.RLIMIT_AS, memory}, {syscall.RLIMIT_FSIZE, r.MaxArtifactBytes + 1}, {syscall.RLIMIT_CORE, 1}} {
		v := rlimit64{l.value, l.value}
		if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRLIMIT64, uintptr(pid), uintptr(l.resource), uintptr(unsafe.Pointer(&v)), 0, 0, 0); errno != 0 {
			return errno
		}
	}
	return nil
}
