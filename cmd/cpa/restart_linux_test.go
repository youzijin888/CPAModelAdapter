//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func fakeProcessStat(parent int, start string) []byte {
	fields := make([]string, 20)
	for index := range fields {
		fields[index] = "0"
	}
	fields[0], fields[1], fields[19] = "S", strconv.Itoa(parent), start
	return []byte("123 (name with ) parentheses) " + strings.Join(fields, " "))
}

func TestSupportedAppServerCommandBoundary(t *testing.T) {
	for _, scenario := range []struct {
		arguments []string
		want      bool
	}{
		{[]string{"codex", "app-server", "--listen", "unix://"}, true},
		{[]string{"codex", "-c", "features.code_mode_host=true", "app-server"}, true},
		{[]string{"codex", "exec", "app-server"}, false},
		{[]string{"codex", "app-server", "-c", "model_provider=other"}, false},
		{[]string{"codex", "app-server", "--profile", "work"}, false},
		{[]string{"codex", "app-server", "--config=model_provider=other"}, false},
		{[]string{"codex", "-c", "features.x=true\nmodel_provider='other'", "app-server"}, false},
	} {
		if result := supportedAppServer([]byte(strings.Join(scenario.arguments, "\x00") + "\x00")); result != scenario.want {
			t.Fatalf("unexpected candidate decision for %q", scenario.arguments)
		}
	}
}

func TestLinuxDiscoveryScopesUserHomeCommandAndAncestors(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "target")
	other := filepath.Join(root, "other")
	os.Mkdir(home, 0700)
	os.Mkdir(other, 0700)
	fixture := func(pid, parent int, executable, processHome, arguments, key string) {
		t.Helper()
		directory := filepath.Join(root, strconv.Itoa(pid))
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		os.Symlink(executable, filepath.Join(directory, "exe"))
		os.WriteFile(filepath.Join(directory, "cmdline"), []byte(arguments), 0600)
		os.WriteFile(filepath.Join(directory, "environ"), []byte("CODEX_HOME="+processHome+"\x00CPA_API_KEY="+key+"\x00"), 0600)
		os.WriteFile(filepath.Join(directory, "stat"), fakeProcessStat(parent, fmt.Sprint(pid+1000)), 0600)
	}
	fixture(100, 200, "/bin/cpa", home, "cpa\x00restart\x00", "")
	fixture(200, 1, "/bin/codex", home, "codex\x00app-server\x00", "fake-key")
	fixture(300, 1, "/bin/codex", other, "codex\x00app-server\x00", "fake-key")
	fixture(400, 1, "/bin/node", home, "node\x00app-server\x00", "fake-key")
	fixture(500, 1, "/bin/codex", home, "codex\x00exec\x00app-server\x00", "fake-key")
	fixture(600, 1, "/bin/codex (deleted)", home, "codex\x00app-server\x00", "stale-key")
	controller := &linuxBackends{root: root, uid: uint32(os.Getuid()), self: 100}
	processes, err := controller.list(filepath.Join(home, "config.toml"), "fake-key")
	if err != nil || len(processes) != 2 {
		t.Fatal("unexpected discovery", processes, err)
	}
	if processes[0].PID != 200 || !processes[0].Ancestor || !processes[0].KeyMatches || processes[1].PID != 600 || processes[1].KeyMatches {
		t.Fatal("incorrect process identity or key classification")
	}
	if err := controller.stop(processes[1]); err == nil {
		t.Fatal("fixture process tree must never send real signals")
	}
	controller.uid++
	processes, err = controller.list(filepath.Join(home, "config.toml"), "fake-key")
	if err != nil || len(processes) != 0 {
		t.Fatal("discovery crossed user boundary")
	}
}
