package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCommitRollbackAndCrashRecovery(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config.toml")
	if e := atomicWrite(p, []byte("old")); e != nil {
		t.Fatal(e)
	}
	blocker := filepath.Join(d, "blocker")
	os.WriteFile(blocker, []byte("file"), 0600)
	// The first file must roll back when a later parent cannot be created.
	if e := commit(d, []Change{{Path: p, Data: []byte("new")}, {Path: filepath.Join(blocker, "child"), Data: []byte("fail")}}); e == nil {
		t.Fatal("expected failure")
	}
	b, _ := os.ReadFile(p)
	if string(b) != "old" {
		t.Fatal("rollback lost config")
	}
	// Simulate an interrupted transaction after a successful first write.
	j := Journal{Originals: []Change{{Path: p, Data: []byte("old")}}}
	atomicWrite(filepath.Join(d, "transaction.json"), marshal(j))
	atomicWrite(p, []byte("partial"))
	if e := recoverTransaction(d); e != nil {
		t.Fatal(e)
	}
	b, _ = os.ReadFile(p)
	if string(b) != "old" {
		t.Fatal("crash recovery")
	}
	if _, e := os.Stat(filepath.Join(d, "transaction.json")); !os.IsNotExist(e) {
		t.Fatal("journal retained")
	}
}

func TestCommitActuallyRollsBackAppliedFile(t *testing.T) {
	d := t.TempDir()
	first := filepath.Join(d, "first")
	os.WriteFile(first, []byte("old"), 0600)
	// Journal preflight permits missing files. Applying the first change creates
	// a regular file which then blocks the second change's parent directory.
	if e := commit(d, []Change{{Path: first, Data: []byte("new")}, {Path: filepath.Join(d, "absent"), Data: []byte("file")}, {Path: filepath.Join(d, "absent", "child"), Data: []byte("x")}}); e == nil {
		t.Fatal("expected commit failure")
	}
	b, _ := os.ReadFile(first)
	if string(b) != "old" {
		t.Fatal("first change not restored")
	}
}

func TestPrivateModesAndLock(t *testing.T) {
	d := filepath.Join(t.TempDir(), "private")
	if e := ensurePrivateDir(d); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(d, "secret")
	if e := atomicWrite(p, []byte("fixture")); e != nil {
		t.Fatal(e)
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{d: 0700, p: 0600} {
			s, _ := os.Stat(path)
			if s.Mode().Perm() != want {
				t.Fatal("permissions", path, s.Mode())
			}
		}
	}
	unlock, e := lockState(filepath.Join(d, ".lock"))
	if e != nil {
		t.Fatal(e)
	}
	if u, e := lockState(filepath.Join(d, ".lock")); e == nil {
		u()
		t.Fatal("second lock acquired")
	}
	unlock()
	unlock, e = lockState(filepath.Join(d, ".lock"))
	if e != nil {
		t.Fatal(e)
	}
	unlock()
}

func TestAtomicWriteRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires a separate Windows privilege")
	}
	d := t.TempDir()
	real := filepath.Join(d, "real")
	link := filepath.Join(d, "link")
	os.WriteFile(real, []byte("original"), 0600)
	os.Symlink(real, link)
	if e := atomicWrite(link, []byte("replacement")); e == nil {
		t.Fatal("followed symlink")
	}
	b, _ := os.ReadFile(real)
	if !bytes.Equal(b, []byte("original")) {
		t.Fatal("real changed")
	}
}
