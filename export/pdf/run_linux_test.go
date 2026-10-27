//go:build linux

package pdf

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	r "idp.local/reporting/reporting"
)

func TestUnchangedDetectsModifiedOrReplacedFiles(t *testing.T) {
	dir := t.TempDir()
	a, b, bin := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "typst")
	for _, p := range []string{a, b, bin} {
		if err := os.WriteFile(p, []byte("one"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer exe.Close()
	w := &Writer{exe: exe, ids: map[string]fileID{}}
	if w.exeID, err = openIdentity(exe); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, a, b} {
		id, err := identity(p)
		if err != nil {
			t.Fatal(err)
		}
		w.ids[p] = id
	}
	if err := w.unchanged(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a, []byte("two!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.unchanged(); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a modified font file: %v; want integrity failure", err)
	}
	w.ids[a], _ = identity(a)
	if err := os.WriteFile(b+".new", []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(b+".new", b); err != nil {
		t.Fatal(err)
	}
	if err := w.unchanged(); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a replaced font file: %v; want integrity failure", err)
	}
	w.ids[b], _ = identity(b)
	w.ids[dir], _ = identity(dir)
	if err := w.unchanged(); err != nil {
		t.Fatal(err)
	}
	// The binary runs from the open file. Writing to it is refused, and so is replacing its
	// path, which unlinks the opened file.
	f, err := os.OpenFile("/proc/self/fd/"+strconv.Itoa(int(exe.Fd())), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("!"))
	f.Close()
	if err := w.unchanged(); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a modified binary: %v; want integrity failure", err)
	}
	w.exeID, _ = openIdentity(exe)
	if err := os.WriteFile(bin+".new", []byte("evil"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(bin+".new", bin); err != nil {
		t.Fatal(err)
	}
	w.ids[dir], _ = identity(dir)
	if err := w.unchanged(); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a replaced binary: %v; want integrity failure", err)
	}
}

func TestDirectoriesOthersCouldReplaceAreRefused(t *testing.T) {
	root := t.TempDir()
	open, sticky := filepath.Join(root, "open"), filepath.Join(root, "sticky")
	for dir, mode := range map[string]os.FileMode{open: 0o777, sticky: 0o777 | os.ModeSticky} {
		if err := os.MkdirAll(filepath.Join(dir, "fonts"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := trustedAncestors(filepath.Join(open, "fonts", "x")); err == nil {
		t.Error("a directory under one that anyone may write is accepted")
	}
	if err := trustedAncestors(filepath.Join(sticky, "fonts", "x")); err != nil {
		t.Errorf("a directory under a sticky one: %v", err)
	}
}

func TestSweepRemovesOnlyStaleRenderDirectories(t *testing.T) {
	work := t.TempDir()
	stale, fresh, other := filepath.Join(work, "reporting-pdf-1"), filepath.Join(work, "reporting-pdf-2"), filepath.Join(work, "keep-me")
	for _, d := range []string{stale, fresh, other} {
		if err := os.MkdirAll(filepath.Join(d, "x"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-staleAfter - time.Minute)
	for _, d := range []string{stale, other} {
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
	}
	sweep(work, time.Now())
	for d, want := range map[string]bool{stale: false, fresh: true, other: true} {
		if _, err := os.Stat(d); (err == nil) != want {
			t.Errorf("%s exists: %v, want %v", filepath.Base(d), err == nil, want)
		}
	}
}
