package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeBackends struct {
	initial     []backendProcess
	replacement []backendProcess
	stopped     int
	onStop      func() error
	onList      func()
	stopError   error
}

func (fake *fakeBackends) list(configPath, key string) ([]backendProcess, error) {
	if fake.onList != nil {
		action := fake.onList
		fake.onList = nil
		action()
	}
	if fake.stopped > 0 && fake.stopped >= len(fake.initial) {
		return fake.replacement, nil
	}
	return fake.initial, nil
}

func (fake *fakeBackends) stop(process backendProcess) error {
	if fake.stopError != nil {
		return fake.stopError
	}
	if fake.onStop != nil {
		if err := fake.onStop(); err != nil {
			return err
		}
	}
	fake.stopped++
	return nil
}

func readyBackends() *fakeBackends {
	return &fakeBackends{
		initial:     []backendProcess{{PID: 101, Start: "1"}},
		replacement: []backendProcess{{PID: 102, Start: "2", KeyMatches: true}},
	}
}

func TestRestartRequiresExplicitConfirmation(t *testing.T) {
	for _, input := range []string{"", "\n", "n\n"} {
		t.Run(strings.TrimSpace(input)+"-declined", func(t *testing.T) {
			harness := newHarness(t)
			harness.initialize(t)
			backend := readyBackends()
			harness.a.backends = backend
			if err := harness.run(input, "restart"); err != nil {
				t.Fatal(err)
			}
			if backend.stopped != 0 || !strings.Contains(harness.out.String(), "未重启后端") {
				t.Fatal("restart happened without affirmative confirmation")
			}
		})
	}
}

func TestRestartReleasesCredentialLockBeforeStopping(t *testing.T) {
	harness := newHarness(t)
	harness.initialize(t)
	backend := readyBackends()
	backend.onStop = func() error {
		unlock, err := lockState(filepath.Join(harness.a.dir, ".lock"))
		if err == nil {
			unlock()
		}
		return err
	}
	harness.a.backends = backend
	if err := harness.run("y\n", "sync"); err != nil {
		t.Fatal(err)
	}
	if backend.stopped != 1 || !strings.Contains(harness.out.String(), "后端重启已验证") {
		t.Fatal("successful mutation did not offer and verify restart", harness.out.String())
	}
	if strings.Contains(harness.out.String(), "fake-test-secret") {
		t.Fatal("restart output leaked key")
	}
}

func TestRestartProtectsAncestorAndChangedConfiguration(t *testing.T) {
	for _, scenario := range []string{"ancestor", "changed"} {
		t.Run(scenario, func(t *testing.T) {
			harness := newHarness(t)
			harness.initialize(t)
			backend := readyBackends()
			harness.a.backends = backend
			if scenario == "ancestor" {
				backend.initial[0].Ancestor = true
			} else {
				backend.onList = func() {
					raw, _ := os.ReadFile(harness.a.config)
					if err := atomicWrite(harness.a.config, append(raw, '\n')); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := harness.run("y\n", "restart"); err == nil || backend.stopped != 0 {
				t.Fatal("unsafe restart was not refused")
			}
		})
	}
}

func TestRestartRequiresAllReplacementsAndMatchingKey(t *testing.T) {
	for _, scenario := range []string{"no-reconnect", "stale-key", "old-process", "partial", "signal-failed"} {
		t.Run(scenario, func(t *testing.T) {
			harness := newHarness(t)
			harness.initialize(t)
			backend := readyBackends()
			switch scenario {
			case "no-reconnect":
				backend.replacement = nil
			case "stale-key":
				backend.replacement[0].KeyMatches = false
			case "old-process":
				backend.replacement = backend.initial
				backend.replacement[0].KeyMatches = true
			case "partial":
				backend.initial = append(backend.initial, backendProcess{PID: 103, Start: "3"})
			case "signal-failed":
				backend.stopError = errors.New("signal refused")
			}
			harness.a.backends = backend
			harness.a.restartTimeout = time.Millisecond
			if err := harness.run("y\n", "restart"); err == nil {
				t.Fatal("unverified restart reported success")
			}
			if strings.Contains(harness.out.String(), "后端重启已验证") {
				t.Fatal("false restart verification")
			}
		})
	}
}

func TestAutomaticRestartFailureKeepsSavedConfiguration(t *testing.T) {
	harness := newHarness(t)
	harness.initialize(t)
	backend := readyBackends()
	backend.stopError = errors.New("signal refused")
	harness.a.backends = backend
	if err := harness.run("y\n", "sync"); err != nil {
		t.Fatal("saved configuration must not be reported as a failed initialization", err)
	}
	if !strings.Contains(harness.out.String(), "配置已保存，但后端重启未完成") {
		t.Fatal("restart failure not surfaced")
	}
	if _, err := readState(harness.a.dir); err != nil {
		t.Fatal("restart failure destroyed saved state", err)
	}
}

func TestFailedMutationDoesNotOfferRestart(t *testing.T) {
	harness := newHarness(t)
	harness.initialize(t)
	backend := readyBackends()
	harness.a.backends = backend
	harness.fail = true
	if err := harness.run("y\n", "sync"); err == nil {
		t.Fatal("expected sync failure")
	}
	if backend.stopped != 0 || strings.Contains(harness.out.String(), "确认现在重启") {
		t.Fatal("failed mutation offered restart")
	}
}
