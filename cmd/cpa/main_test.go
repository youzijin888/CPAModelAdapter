package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type harness struct {
	a     *App
	out   *bytes.Buffer
	fail  bool
	calls int
	url   string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	d := t.TempDir()
	template := filepath.Join(d, "template.json")
	if e := os.WriteFile(template, marshal(fixture()), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("CPA_TEMPLATE_FILE", template)
	h := &harness{out: &bytes.Buffer{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.calls++
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer fake-test-secret" {
			t.Error("authorization mismatch")
		}
		if h.fail {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"known"},{"id":"gpt-6-astra"},{"id":"gpt-image-2"}]}`)
	}))
	t.Cleanup(server.Close)
	h.url = server.URL + "/v1"
	h.a = &App{dir: filepath.Join(d, "data"), config: filepath.Join(d, "codex", "config.toml"), out: h.out, client: server.Client(), backends: &fakeBackends{}, secret: func() (string, error) { return "fake-test-secret", nil }}
	return h
}

func (h *harness) run(input string, args ...string) error {
	h.out.Reset()
	h.a.in = bufio.NewReader(strings.NewReader(input))
	return h.a.run(args)
}
func (h *harness) initialize(t *testing.T) {
	t.Helper()
	if e := h.run("y\n"+h.url+"\n1\n", "init"); e != nil {
		t.Fatal(e, h.out.String())
	}
}

func TestFullNumberedLifecycle(t *testing.T) {
	h := newHarness(t)
	h.initialize(t)
	initialConfig, _ := os.ReadFile(h.a.config)
	if e := h.run(h.url+"\nn\n", "add"); e != nil {
		t.Fatal(e)
	}
	s, e := readState(h.a.dir)
	if e != nil {
		t.Fatal(e)
	}
	if s.Current != 1 || s.NextID != 3 || len(s.Providers) != 2 {
		t.Fatal("add changed selection", s.Current, s.NextID)
	}
	now, _ := os.ReadFile(h.a.config)
	if !bytes.Equal(initialConfig, now) {
		t.Fatal("add altered Codex config")
	}
	if e = h.run("", "use", "2"); e != nil {
		t.Fatal(e)
	}
	s, _ = readState(h.a.dir)
	if s.Current != 2 {
		t.Fatal("switch failed")
	}
	if e = h.run("y\n", "delete", "1"); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(catalogPath(h.a.dir, 1)); !os.IsNotExist(e) {
		t.Fatal("deleted model file remains")
	}
	if e = h.run(h.url+"\nn\n", "add"); e != nil {
		t.Fatal(e)
	}
	s, _ = readState(h.a.dir)
	if s.Providers[1].ID != 3 || s.NextID != 4 {
		t.Fatal("number reused")
	}
	if e = h.run("y\n3\n", "delete", "2"); e != nil {
		t.Fatal(e)
	}
	s, _ = readState(h.a.dir)
	if s.Current != 3 || len(s.Providers) != 1 {
		t.Fatal("delete current didn't switch")
	}
	if e = h.run("y\n", "delete", "3"); e == nil {
		t.Fatal("deleted only provider")
	}
	if e = h.run("", "sync"); e != nil {
		t.Fatal(e)
	}
}

func TestFailedSwitchAndDeleteKeepAllFiles(t *testing.T) {
	h := newHarness(t)
	h.initialize(t)
	if e := h.run(h.url+"\nn\n", "add"); e != nil {
		t.Fatal(e)
	}
	statePath := filepath.Join(h.a.dir, "providers.json")
	state, _ := os.ReadFile(statePath)
	config, _ := os.ReadFile(h.a.config)
	cat, _ := os.ReadFile(catalogPath(h.a.dir, 1))
	h.fail = true
	for _, tc := range []struct {
		input string
		args  []string
	}{{"", []string{"use", "2"}}, {"y\n2\n", []string{"delete", "1"}}} {
		if e := h.run(tc.input, tc.args...); e == nil {
			t.Fatal("expected network failure")
		}
		for p, want := range map[string][]byte{statePath: state, h.a.config: config, catalogPath(h.a.dir, 1): cat} {
			got, _ := os.ReadFile(p)
			if !bytes.Equal(got, want) {
				t.Fatal("failed request mutated", p)
			}
		}
	}
}

func TestReadCommandsNeverNetworkOrLeakSecrets(t *testing.T) {
	h := newHarness(t)
	h.initialize(t)
	calls := h.calls
	for _, cmd := range []string{"status", "list"} {
		if e := h.run("", cmd); e != nil {
			t.Fatal(e)
		}
		if strings.Contains(h.out.String(), "fake-test-secret") {
			t.Fatal("secret leaked")
		}
	}
	if h.calls != calls {
		t.Fatal("read command made network call")
	}
	if e := h.run("", "_key"); e == nil {
		t.Fatal("unwrapped secret access allowed")
	}
}

func TestExistingMigrationAndCancellation(t *testing.T) {
	h := newHarness(t)
	unrelated := "[mcp_servers.demo]\ncommand='leave-me'\n[features]\nsomething=true\n"
	original := []byte("model_provider='custom'\nmodel='known'\n[model_providers.custom]\nbase_url='" + h.url + "'\nenv_key='CPA_TEST_OLD_KEY'\n" + unrelated)
	if e := atomicWrite(h.a.config, original); e != nil {
		t.Fatal(e)
	}
	t.Setenv("CPA_TEST_OLD_KEY", "fake-test-secret")
	h.a.secret = func() (string, error) { return "", nil }
	if e := h.run("n\n", "init"); e == nil {
		t.Fatal("expected cancel")
	}
	now, _ := os.ReadFile(h.a.config)
	if !bytes.Equal(now, original) {
		t.Fatal("cancel changed config")
	}
	if e := h.run("y\n\n", "init"); e != nil {
		t.Fatal(e, h.out.String())
	}
	now, _ = os.ReadFile(h.a.config)
	if !bytes.Contains(now, []byte(unrelated)) {
		t.Fatal("migration changed unrelated config")
	}
	if strings.Contains(h.out.String(), "fake-test-secret") {
		t.Fatal("migration leaked secret")
	}
	state, err := readState(h.a.dir)
	if err != nil || state.codexProvider() != "custom" {
		t.Fatal("migration did not persist existing provider ID")
	}
	if err = h.run("", "sync"); err != nil {
		t.Fatal(err)
	}
	_, configuration, err := loadConfig(h.a.config)
	if err != nil || str(configuration["model_provider"]) != "custom" {
		t.Fatal("sync changed existing provider ID")
	}
	t.Setenv("CPA_SHELL_INTEGRATED", "1")
	if err = h.run("", "_key"); err != nil || h.out.String() != "fake-test-secret" {
		t.Fatal("credential loader did not support preserved provider ID")
	}
	s, _ := readState(h.a.dir)
	if s.Providers[0].Key != "fake-test-secret" {
		t.Fatal("old key not imported")
	}
	if e := h.run("y\n", "init"); e == nil {
		t.Fatal("reinitialized existing state")
	}
}

func TestModelMismatchRequiresExplicitSelection(t *testing.T) {
	h := newHarness(t)
	original := []byte("model='missing'\nmodel_reasoning_effort='ultra'\n")
	atomicWrite(h.a.config, original)
	if e := h.run("y\n"+h.url+"\n1\nno-such-effort\n", "init"); e == nil {
		t.Fatal("accepted wrong effort")
	}
	b, _ := os.ReadFile(h.a.config)
	if !bytes.Equal(b, original) {
		t.Fatal("changed on invalid choice")
	}
	if e := h.run("y\n"+h.url+"\n1\nhigh\n", "init"); e != nil {
		t.Fatal(e)
	}
	_, cfg, _ := loadConfig(h.a.config)
	if str(cfg["model"]) != "known" || str(cfg["model_reasoning_effort"]) != "high" {
		t.Fatal("selection ignored")
	}
}

func TestCLIHelpAndArguments(t *testing.T) {
	h := newHarness(t)
	for _, flag := range []string{"-help", "--help", "-h"} {
		if e := h.run("", flag); e != nil {
			t.Fatal(e)
		}
	}
	for _, args := range [][]string{nil, {"unknown"}, {"use"}, {"use", "2", "3"}, {"status", "2"}, {"add", "secret"}} {
		if e := h.run("", args...); e == nil {
			t.Fatal("invalid args accepted", args)
		}
	}
	if _, e := os.Stat(h.a.dir); !os.IsNotExist(e) {
		t.Fatal("help/invalid args created state")
	}
}

func TestConfigPathMismatchAndExternalChange(t *testing.T) {
	h := newHarness(t)
	h.initialize(t)
	orig := h.a.config
	h.a.config = filepath.Join(t.TempDir(), "config.toml")
	if e := h.run("", "status"); e == nil {
		t.Fatal("path mismatch ignored")
	}
	h.a.config = orig
	atomicWrite(orig, []byte("model_provider='external'\n"))
	if e := h.run("", "sync"); e == nil {
		t.Fatal("external ownership overwritten")
	}
	b, _ := os.ReadFile(orig)
	if string(b) != "model_provider='external'\n" {
		t.Fatal("external file changed")
	}
}

func TestReadRefusesIncompleteTransaction(t *testing.T) {
	h := newHarness(t)
	h.initialize(t)
	atomicWrite(filepath.Join(h.a.dir, "transaction.json"), marshal(Journal{Originals: []Change{{Path: h.a.config, Data: mustConfigBytes(h.a.config)}}}))
	if e := h.run("", "status"); e == nil {
		t.Fatal("read incomplete state")
	}
	if e := h.run("", "sync"); e != nil {
		t.Fatal(e)
	}
}

func TestFreshInitWithOnlyProviderNetworkAllowed(t *testing.T) {
	h := newHarness(t)
	t.Setenv("CPA_TEMPLATE_FILE", "")
	transport := h.a.client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	h.a.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != h.url+"/models" {
			t.Errorf("unexpected external request: %s", r.URL.Host)
			return nil, fmt.Errorf("external network blocked")
		}
		return transport.RoundTrip(r)
	})}
	h.initialize(t)
	if h.calls != 1 {
		t.Fatal("initialization should make exactly one provider request")
	}
	if e := h.run("", "sync"); e != nil {
		t.Fatal(e)
	}
	if h.calls != 2 {
		t.Fatal("sync contacted an unexpected endpoint")
	}
}
