package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMergePreservesUnrelatedBytes(t *testing.T) {
	unrelated := `# leave every byte alone
[mcp_servers.demo]
command = "my-mcp"
args = ["a", "b"] # keep comment
note = """
[model_providers.fake]
model = "not a setting"
"""
[projects."/path with spaces"]
trust_level = "trusted"
[model_providers.other]
name = "untouched"
base_url = "https://other.test/v1"
env_key = "OTHER_KEY"
`
	old := []byte("# top\n\"model_provider\" = 'old' # keep this\nmodel = \"known\"\nmodel_reasoning_effort = \"high\"\n[model_providers.'old']\nname = 'old'\nbase_url = 'https://old.test/v1'\nenv_key = 'OLD_KEY'\n" + unrelated)
	out, e := mergeConfig(old, Provider{URL: "https://new.test/v1"}, "C:\\users\\名字 with space\\2.json", "known", "", true, "old")
	if e != nil {
		t.Fatal(e)
	}
	// The comment immediately before another table belongs to the removed
	// provider block, but all actual unrelated table bytes must survive.
	if !bytes.Contains(out, []byte(strings.TrimPrefix(unrelated, "# leave every byte alone\n"))) {
		t.Fatalf("unrelated bytes changed: %s", out)
	}
	if !bytes.Contains(out, []byte("# keep this")) {
		t.Fatal("root comment lost")
	}
	c, e := configData(out)
	if e != nil {
		t.Fatal(e)
	}
	if str(c["model_reasoning_effort"]) != "high" {
		t.Fatal("effort changed")
	}
	ps := asMap(c["model_providers"])
	if ps["old"] == nil || ps["other"] == nil || ps["cpa"] != nil || str(c["model_provider"]) != "old" {
		t.Fatal("provider scopes incorrect")
	}
	if str(asMap(ps["old"])["name"]) != "old" {
		t.Fatal("provider display name changed")
	}
	if str(c["model_catalog_json"]) != "C:\\users\\名字 with space\\2.json" {
		t.Fatal("path escaping")
	}
}

func TestMergeRefusesSharedProviderMigration(t *testing.T) {
	old := []byte("model_provider='old'\n[model_providers.old]\nenv_key='OLD'\n[profiles.work]\nmodel_provider='old'\n")
	if _, err := mergeConfig(old, Provider{URL: "https://new.test/v1"}, "/models.json", "known", "", true, "old"); err == nil {
		t.Fatal("shared provider migration must require explicit resolution")
	}
}

func TestMergeCRLFAndNoFinalNewline(t *testing.T) {
	old := []byte("model_provider=\"cpa\"\r\nmodel = 'old'\r\n[model_providers.cpa]\r\nname='CPA'\r\n[features]\r\nx = true")
	out, e := mergeConfig(old, Provider{URL: "https://new.test/v1"}, "/a.json", "new", "", false, "cpa")
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(out, []byte("[features]\r\nx = true")) {
		t.Fatal("CRLF changed")
	}
	parsed, _ := configData(out)
	if len(asMap(parsed["model_providers"])) != 1 || asMap(parsed["model_providers"])["cpa"] == nil {
		t.Fatal("duplicate cpa")
	}
}

func TestMergeRefusesUnsafeAndOverriddenConfigurations(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		migrate    bool
	}{
		{"invalid", "x='unterminated", true},
		{"inline", "model_providers={old={env_key='K'}}", true},
		{"dotted", "model_providers.old.env_key='K'", true},
		{"conflict", "model_provider='old'\n[model_providers.cpa]\nenv_key='K'", true},
		{"external", "model_provider='other'", false},
		{"profile", "profile='work'", true},
		{"nonstring", "model=123", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := mergeConfig([]byte(tc.text), Provider{URL: "https://x.test/v1"}, "/m", "m", "", tc.migrate, "cpa"); e == nil {
				t.Fatal("expected refusal")
			}
		})
	}
}

func TestMergeEmptyAndIdempotent(t *testing.T) {
	p := Provider{URL: "https://x.test/v1"}
	a, e := mergeConfig(nil, p, "/m", "known", "", true, "cpa")
	if e != nil {
		t.Fatal(e)
	}
	b, e := mergeConfig(a, p, "/m", "known", "", false, "cpa")
	if e != nil {
		t.Fatal(e)
	}
	ca, _ := configData(a)
	cb, _ := configData(b)
	if !bytes.Equal(marshal(ca), marshal(cb)) {
		t.Fatal("semantic idempotency failed")
	}
}
