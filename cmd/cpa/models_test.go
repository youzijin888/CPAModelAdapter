package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBundledCatalogValidWithoutNetworkOrCache(t *testing.T) {
	t.Setenv("CPA_TEMPLATE_FILE", "")
	calls := 0
	a := &App{dir: t.TempDir(), client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { calls++; return nil, fmt.Errorf("network blocked") })}}
	catalog, e := a.templates()
	if e != nil {
		t.Fatal(e)
	}
	if len(catalog.Models) != 11 {
		t.Fatal("unexpected bundled model count")
	}
	if calls != 0 {
		t.Fatal("template retrieval contacted network")
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(bundledModels)); got != "d7136a413cfac1b5b1686d9e0dcc5c80ca05bebed5e9fc3911376561d0ef6ee8" {
		t.Fatal("bundled source checksum changed; update provenance deliberately")
	}
	for _, m := range catalog.Models {
		if str(m["slug"]) == "gpt-6-astra" {
			if str(m["base_instructions"]) != str(asMap(m["model_messages"])["instructions_template"]) {
				t.Fatal("native model instructions lost")
			}
			if len(efforts(m)) < 2 {
				t.Fatal("native reasoning metadata lost")
			}
		}
	}
	if _, e := os.Stat(filepath.Join(a.dir, "template-cache.json")); !os.IsNotExist(e) {
		t.Fatal("unexpected template cache dependency")
	}
}

func TestModernAndLegacyTemplateFormats(t *testing.T) {
	f := fixture()
	for name, data := range map[string][]byte{"legacy": marshal(f), "modern": marshal(Catalog{Models: f.Models})} {
		t.Run(name, func(t *testing.T) {
			templates, e := decodeTemplates(data)
			if e != nil {
				t.Fatal(e)
			}
			catalog, _, e := buildCatalog([]map[string]any{{"slug": "known"}, {"slug": "unknown"}}, templates)
			if e != nil {
				t.Fatal(e)
			}
			if len(catalog.Models) != 2 {
				t.Fatal("model generation failed")
			}
			if len(catalog.Models[0]["service_tiers"].([]any)) != 1 {
				t.Fatal("exact model metadata lost")
			}
			if name == "modern" && catalog.Models[1]["context_window"] != float64(32768) {
				t.Fatal("unknown model inherited oversized context")
			}
		})
	}
}

func TestInvalidExplicitTemplateReportsFormatNotNetwork(t *testing.T) {
	p := filepath.Join(t.TempDir(), "invalid.json")
	os.WriteFile(p, []byte(`{"models":[]}`), 0600)
	t.Setenv("CPA_TEMPLATE_FILE", p)
	a := &App{}
	_, e := a.templates()
	if e == nil || !strings.Contains(e.Error(), "格式无效") || strings.Contains(e.Error(), "网络") {
		t.Fatal("misleading format error", e)
	}
}

func fixture() Templates {
	f := map[string]any{
		"slug": "known", "display_name": "known", "description": "fixture",
		"base_instructions": "You are a helpful coding assistant. Follow the user's instructions.",
		"context_window":    float64(32768), "max_context_window": float64(32768),
		"supported_reasoning_levels": []any{map[string]any{"effort": "medium", "description": "medium"}, map[string]any{"effort": "high", "description": "high"}},
		"shell_type":                 "shell_command", "visibility": "list", "supported_in_api": true,
		"default_reasoning_summary": "none", "support_verbosity": false,
		"truncation_policy":            map[string]any{"mode": "tokens", "limit": float64(10000)},
		"supports_parallel_tool_calls": true, "experimental_supported_tools": []any{}, "priority": float64(1),
		"service_tiers": []any{"fast"}, "additional_speed_tiers": []any{"fast"},
	}
	return Templates{Fallback: clone(f), Models: []map[string]any{f}}
}

func TestCatalogFilteringAndEfforts(t *testing.T) {
	defs := []map[string]any{
		{"slug": "known", "context_length": float64(65536)},
		{"slug": "gpt-6-astra"},
		{"slug": "new", "thinking": map[string]any{"levels": []any{"low", "high", "high", "invalid"}}},
		{"slug": "gpt-image-2"},
		{"slug": "codex-auto-review"},
		{"slug": "not-tools", "supported_parameters": []any{"temperature"}},
	}
	c, n, e := buildCatalog(defs, fixture())
	if e != nil {
		t.Fatal(e)
	}
	if n != 3 || len(c.Models) != 3 {
		t.Fatal(n, len(c.Models))
	}
	if c.Models[0]["context_window"] != float64(65536) {
		t.Fatal("context metadata")
	}
	if len(c.Models[0]["service_tiers"].([]any)) != 1 {
		t.Fatal("exact speed tier lost")
	}
	if !includes(efforts(c.Models[1]), "max") {
		t.Fatal("Astra effort regression")
	}
	if len(c.Models[1]["service_tiers"].([]any)) != 0 {
		t.Fatal("invented fast support")
	}
	if len(efforts(c.Models[2])) != 2 {
		t.Fatal("effort dedup")
	}
}

func TestUnknownModelsWithoutThinkingGetAllCodexEfforts(t *testing.T) {
	for _, id := range []string{"gpt-6-sol", "gpt-6-luna", "future-model"} {
		t.Run(id, func(t *testing.T) {
			catalog, _, err := buildCatalog([]map[string]any{{"slug": id}}, fixture())
			if err != nil {
				t.Fatal(err)
			}
			model := catalog.Models[0]
			if got := efforts(model); !reflect.DeepEqual(got, allEfforts) {
				t.Fatalf("efforts = %v, want %v", got, allEfforts)
			}
			if str(model["default_reasoning_level"]) != "medium" {
				t.Fatal("unknown model default effort must be medium")
			}
		})
	}
}

func TestReasoningPriorityAndDeepSeekCompatibility(t *testing.T) {
	t.Run("live metadata wins", func(t *testing.T) {
		catalog, _, err := buildCatalog([]map[string]any{{"slug": "future-model", "thinking": map[string]any{"levels": []any{"low", "max"}}}}, fixture())
		if err != nil {
			t.Fatal(err)
		}
		if got := efforts(catalog.Models[0]); !reflect.DeepEqual(got, []string{"low", "max"}) {
			t.Fatalf("live efforts = %v", got)
		}
	})

	t.Run("exact template wins", func(t *testing.T) {
		catalog, _, err := buildCatalog([]map[string]any{{"slug": "known"}}, fixture())
		if err != nil {
			t.Fatal(err)
		}
		if got := efforts(catalog.Models[0]); !reflect.DeepEqual(got, []string{"medium", "high"}) {
			t.Fatalf("template efforts = %v", got)
		}
	})

	t.Run("deepseek fallback", func(t *testing.T) {
		catalog, _, err := buildCatalog([]map[string]any{{"slug": "deepseek-flash"}}, fixture())
		if err != nil {
			t.Fatal(err)
		}
		model := catalog.Models[0]
		if got := efforts(model); !reflect.DeepEqual(got, allEfforts) {
			t.Fatalf("DeepSeek efforts = %v", got)
		}
		if str(model["default_reasoning_level"]) != "high" {
			t.Fatal("DeepSeek default effort must be high")
		}
	})
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{"https://x.test": "https://x.test/v1", "https://x.test/v1/": "https://x.test/v1", "https://x.test/custom": "https://x.test/custom"} {
		got, e := normalizeURL(in)
		if e != nil || got != want {
			t.Fatal(in, got, e)
		}
	}
	for _, in := range []string{"ftp://x.test", "https://user:secret@x.test", "https://x.test?key=secret", "https://x.test/#a", "https://x.test/v1/models", "x.test"} {
		if _, e := normalizeURL(in); e == nil {
			t.Fatal("accepted unsafe URL", in)
		}
	}
}

func TestHTTPDoesNotForwardKeyOnRedirect(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	var v any
	if e := fetchJSON(httpClient(), source.URL, "fake-test-key", &v); e == nil {
		t.Fatal("redirect accepted")
	}
	if targetCalls != 0 {
		t.Fatal("followed redirect")
	}
}

func TestDefinitionsDedupAndValidation(t *testing.T) {
	var payload any
	json.Unmarshal([]byte(`{"data":[{"id":"A"},{"id":"a"},"B",{"name":"C"},{"id":"\u001bBAD"}]}`), &payload)
	d, e := definitions(payload)
	if e != nil || len(d) != 3 {
		t.Fatal(d, e)
	}
	if _, _, e = buildCatalog([]map[string]any{{"slug": "gpt-image-2"}}, fixture()); e == nil {
		t.Fatal("accepted empty catalog")
	}
	if e = validateTemplates(Templates{}); e == nil {
		t.Fatal("accepted broken template")
	}
}
