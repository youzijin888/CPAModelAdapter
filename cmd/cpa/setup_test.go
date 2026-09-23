package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHookInsertionIdempotent(t *testing.T) {
	old := []byte("# user settings\r\nexport OTHER=value\r\n")
	a, e := appendHook(old, ". '/a b/shell.sh'")
	if e != nil {
		t.Fatal(e)
	}
	b, e := appendHook(a, ". '/a b/shell.sh'")
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(a, b) || !bytes.HasPrefix(b, old) {
		t.Fatal("hook rewrite altered user content")
	}
	if _, e = appendHook([]byte(hookStart), "x"); e == nil {
		t.Fatal("accepted incomplete marker")
	}
}

func TestBashHookRefreshesParentWithoutEvalOrSecretTrace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash hook is tested on Unix")
	}
	bash, e := exec.LookPath("bash")
	if e != nil {
		t.Skip("bash unavailable")
	}
	d := t.TempDir()
	binary := filepath.Join(d, "cpa fixture ' with spaces")
	// A fake binary returns deliberately shell-shaped credentials. They must
	// become data, not commands, and must not appear in xtrace output.
	key := "fake-$(echo INJECTED)-'key"
	script := "#!/bin/sh\nif [ \"$1\" = _key ]; then printf %s " + shQuote(key) + "; fi\n"
	if e = os.WriteFile(binary, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	hook := filepath.Join(d, "shell.sh")
	os.WriteFile(hook, []byte(shellHook(binary)), 0600)
	program := "set -x\n. " + shQuote(hook) + "\ncpa use 2\nset +x\n[ \"$CPA_API_KEY\" = " + shQuote(key) + " ] && printf PASS"
	cmd := exec.Command(bash, "--noprofile", "--norc", "-c", program)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if e = cmd.Run(); e != nil {
		t.Fatal("hook failed", e)
	}
	if out.String() != "PASS" {
		t.Fatal("bad result", out.String())
	}
	if strings.Contains(stderr.String(), key) || strings.Contains(stderr.String(), "INJECTED") {
		t.Fatal("secret leaked to xtrace")
	}
}

func TestInstallerInIsolatedHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PowerShell profile discovery needs Windows integration testing")
	}
	h := t.TempDir()
	old := []byte("# custom configuration\nexport UNRELATED='unchanged'\n")
	os.WriteFile(filepath.Join(h, ".bashrc"), old, 0600)
	os.WriteFile(filepath.Join(h, ".profile"), old, 0600)
	out := &bytes.Buffer{}
	a := &App{dir: filepath.Join(h, ".cpa"), out: out}
	if e := a.setupAt(h); e != nil {
		t.Fatal(e)
	}
	if e := a.setupAt(h); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{".bashrc", ".profile"} {
		b, e := os.ReadFile(filepath.Join(h, p))
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Contains(b, old) || bytes.Count(b, []byte(hookStart)) != 1 {
			t.Fatal("installer damaged or duplicated hook", p)
		}
	}
	if _, e := os.Stat(filepath.Join(h, ".bash_profile")); !os.IsNotExist(e) {
		t.Fatal("created shadowing login file")
	}
	if _, e := os.Stat(filepath.Join(h, ".local", "bin", "cpa")); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(a.dir, "providers.json")); !os.IsNotExist(e) {
		t.Fatal("installer initialized credentials")
	}
}

func TestPrependHookBeforeNoninteractiveReturn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash startup is tested on Unix")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	old := []byte("case $- in *i*) ;; *) return ;; esac\nexport UNRELATED=unchanged\n")
	legacy, err := appendHook(old, "export CPA_API_KEY=fake-startup-key")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := prependHook(legacy, "export CPA_API_KEY=fake-startup-key")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := prependHook(updated, "export CPA_API_KEY=fake-startup-key")
	if err != nil || !bytes.Equal(updated, repeated) || !bytes.Contains(updated, old) {
		t.Fatal("startup hook is not byte-preserving and idempotent")
	}
	startup := filepath.Join(t.TempDir(), "bashrc")
	if err = os.WriteFile(startup, updated, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(bash, "--noprofile", "--norc", "-c", "unset CPA_API_KEY; . "+shQuote(startup)+"; [ \"$CPA_API_KEY\" = fake-startup-key ]")
	if err = command.Run(); err != nil {
		t.Fatal("noninteractive startup did not load credentials", err)
	}
}

func TestPrependHookPreservesBOMAndCRLF(t *testing.T) {
	old := []byte("\xef\xbb\xbf# settings\r\nreturn\r\n")
	first, err := prependHook(old, "true")
	if err != nil {
		t.Fatal(err)
	}
	second, err := prependHook(first, "true")
	if err != nil || !bytes.Equal(first, second) || !bytes.HasPrefix(first, []byte("\xef\xbb\xbf"+hookStart+"\r\n")) || !bytes.HasSuffix(first, old[3:]) {
		t.Fatal("BOM or CRLF changed")
	}
}
