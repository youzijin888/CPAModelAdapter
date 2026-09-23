package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
	"unicode"
)

const maxJSON = 8 << 20

// Pinned OpenAI Codex catalog, Apache-2.0. See assets/README.md for provenance.
// Template selection never makes an HTTP request or depends on a warm cache.
//
//go:embed assets/codex-models.json
var bundledModels []byte

var requiredFields = []string{"slug", "display_name", "base_instructions", "context_window", "max_context_window", "supported_reasoning_levels", "shell_type", "visibility", "supported_in_api", "default_reasoning_summary", "support_verbosity", "truncation_policy", "supports_parallel_tool_calls", "experimental_supported_tools", "priority"}
var allowedEfforts = map[string]bool{"none": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true, "ultra": true}

type Catalog struct {
	Models []map[string]any `json:"models"`
}
type Templates struct {
	Fallback map[string]any   `json:"fallback_model"`
	Models   []map[string]any `json:"models"`
}

func control(s string) bool { return strings.IndexFunc(s, unicode.IsControl) >= 0 }
func normalizeURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	u, e := url.Parse(s)
	if e != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || control(s) {
		return "", errors.New("请输入 http(s) API 基础地址，不能包含账号、密钥、查询参数或片段")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/v1"
	}
	if strings.HasSuffix(u.Path, "/models") {
		return "", errors.New("请输入 API 基础地址（如 /v1），不是 /models 接口地址")
	}
	return u.String(), nil
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
}

func fetchJSON(client *http.Client, endpoint, key string, out any) error {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("无法创建接口请求")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CPAModelAdapter/2.0")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("连接失败（网络、TLS 或超时）；未打印请求或凭据")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("接口返回 HTTP %d；不跟随跳转、不输出响应正文", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxJSON+1))
	if err != nil {
		return errors.New("读取接口响应失败")
	}
	if len(b) > maxJSON {
		return errors.New("接口响应过大")
	}
	if json.Unmarshal(b, out) != nil {
		return errors.New("接口未返回有效 JSON")
	}
	return nil
}

func validateEntry(m map[string]any, slug bool) error {
	for _, k := range requiredFields {
		if k == "slug" && !slug {
			continue
		}
		if _, ok := m[k]; !ok {
			return fmt.Errorf("模型模板缺少字段 %s", k)
		}
	}
	if strings.TrimSpace(str(m["base_instructions"])) == "" {
		return errors.New("模型模板缺少基础指令")
	}
	c, _ := m["context_window"].(float64)
	mx, _ := m["max_context_window"].(float64)
	if c <= 0 || mx < c {
		return errors.New("模型模板上下文长度无效")
	}
	if _, ok := m["supported_reasoning_levels"].([]any); !ok {
		return errors.New("推理档位格式无效")
	}
	if _, ok := m["experimental_supported_tools"].([]any); !ok {
		return errors.New("工具元数据格式无效")
	}
	if slug && (str(m["slug"]) == "" || control(str(m["slug"]))) {
		return errors.New("模型名称为空或包含控制字符")
	}
	return nil
}

func validateTemplates(t Templates) error {
	if len(t.Models) == 0 {
		return errors.New("模型模板列表为空")
	}
	if err := validateEntry(t.Fallback, false); err != nil {
		return err
	}
	for _, m := range t.Models {
		if err := validateEntry(m, true); err != nil {
			return err
		}
	}
	return nil
}

func validateCatalog(c Catalog) error {
	if len(c.Models) == 0 {
		return errors.New("没有可用的文本/工具模型")
	}
	seen := map[string]bool{}
	for _, m := range c.Models {
		if err := validateEntry(m, true); err != nil {
			return err
		}
		id := strings.ToLower(str(m["slug"]))
		if seen[id] {
			return errors.New("模型列表中存在重复编号")
		}
		seen[id] = true
	}
	return nil
}

// Newer catalogs have only models, and some entries place their base prompt in
// model_messages.instructions_template. Normalize both formats before validating.
func decodeTemplates(data []byte) (Templates, error) {
	var t Templates
	if json.Unmarshal(data, &t) != nil {
		return t, errors.New("模型模板不是有效 JSON")
	}
	for _, m := range t.Models {
		if strings.TrimSpace(str(m["base_instructions"])) == "" {
			if instructions := str(asMap(m["model_messages"])["instructions_template"]); instructions != "" {
				m["base_instructions"] = instructions
			}
		}
	}
	if t.Fallback == nil {
		t.Fallback = conservativeFallback()
	}
	return t, validateTemplates(t)
}

// An unknown model must not inherit another model's advanced tool policies,
// speed tiers, or enormous context window. Real /models metadata still wins.
func conservativeFallback() map[string]any {
	return map[string]any{
		"display_name":      "Compatible Model",
		"description":       "Unknown model; conservative compatibility metadata",
		"base_instructions": "You are a coding assistant. Follow the user's instructions, inspect relevant files before editing, preserve unrelated changes, and verify your work. Use the tools provided by the client when appropriate.",
		"context_window":    float64(32768), "max_context_window": float64(32768),
		"supported_reasoning_levels": []any{map[string]any{"effort": "medium", "description": "medium reasoning effort"}},
		"default_reasoning_level":    "medium", "shell_type": "default", "visibility": "list",
		"supported_in_api": true, "default_reasoning_summary": "none", "support_verbosity": false,
		"truncation_policy":            map[string]any{"mode": "tokens", "limit": float64(10000)},
		"supports_parallel_tool_calls": false, "experimental_supported_tools": []any{}, "priority": float64(1),
		"input_modalities": []any{"text"}, "supports_image_detail_original": false,
	}
}

func (a *App) templates() (Templates, error) {
	if p := os.Getenv("CPA_TEMPLATE_FILE"); p != "" {
		b, e := os.ReadFile(p)
		if e != nil {
			return Templates{}, errors.New("无法读取指定的模型模板")
		}
		t, err := decodeTemplates(b)
		if err != nil {
			return t, fmt.Errorf("指定的模型模板格式无效：%w", err)
		}
		return t, nil
	}
	t, err := decodeTemplates(bundledModels)
	if err != nil {
		return t, fmt.Errorf("程序内置模型模板无效，请更新 CPA：%w", err)
	}
	return t, nil
}

func definitions(payload any) ([]map[string]any, error) {
	var values []any
	if m, ok := payload.(map[string]any); ok {
		values, _ = m["data"].([]any)
		if len(values) == 0 {
			values, _ = m["models"].([]any)
		}
	} else {
		values, _ = payload.([]any)
	}
	if values == nil {
		return nil, errors.New("模型响应没有 data/models 数组")
	}
	var result []map[string]any
	seen := map[string]bool{}
	for _, v := range values {
		m := asMap(v)
		id := str(v)
		for _, k := range []string{"slug", "id", "name", "model", "value"} {
			if id == "" {
				id = str(m[k])
			}
		}
		id = strings.TrimSpace(id)
		if id == "" || control(id) || seen[strings.ToLower(id)] {
			continue
		}
		seen[strings.ToLower(id)] = true
		if m == nil {
			m = map[string]any{}
		}
		m["slug"] = id
		result = append(result, m)
	}
	return result, nil
}

func stringList(v any) []string {
	var r []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok {
				r = append(r, strings.ToLower(s))
			}
		}
	}
	return r
}
func includes(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
func excluded(m map[string]any) bool {
	id := strings.ToLower(str(m["slug"]))
	for _, pattern := range []string{"gpt-image-*", "*-image-*", "*embedding*", "*audio*", "*realtime*", "codex-auto-review"} {
		if ok, _ := path.Match(pattern, id); ok {
			return true
		}
	}
	for _, key := range []string{"supportedOutputModalities", "supported_output_modalities"} {
		if l := stringList(m[key]); len(l) > 0 && !includes(l, "text") {
			return true
		}
	}
	if l := stringList(m["supported_parameters"]); len(l) > 0 && !includes(l, "tools") {
		return true
	}
	return false
}

func clone(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var c map[string]any
	json.Unmarshal(b, &c)
	return c
}
func efforts(m map[string]any) []string {
	var levels []string
	if l, ok := m["supported_reasoning_levels"].([]any); ok {
		for _, v := range l {
			s := str(asMap(v)["effort"])
			if allowedEfforts[s] && !includes(levels, s) {
				levels = append(levels, s)
			}
		}
	}
	return levels
}

func buildCatalog(defs []map[string]any, t Templates) (Catalog, int, error) {
	c := Catalog{}
	skipped := 0
	known := map[string]map[string]any{}
	for _, m := range t.Models {
		known[strings.ToLower(str(m["slug"]))] = m
	}
	for _, d := range defs {
		if excluded(d) {
			skipped++
			continue
		}
		id := str(d["slug"])
		base, exact := known[strings.ToLower(id)]
		if !exact {
			base = t.Fallback
		}
		m := clone(base)
		m["slug"] = id
		m["display_name"] = id
		m["visibility"] = "list"
		m["priority"] = len(c.Models) + 1
		if s := str(d["display_name"]); s != "" {
			m["display_name"] = s
		}
		if s := str(d["description"]); s != "" {
			m["description"] = s
		}
		context, _ := d["context_length"].(float64)
		if context <= 0 {
			context, _ = d["context_window"].(float64)
		}
		if context > 0 {
			m["context_window"] = context
			if mx, _ := m["max_context_window"].(float64); mx < context {
				m["max_context_window"] = context
			}
		}
		inputs := stringList(d["supportedInputModalities"])
		if len(inputs) == 0 {
			inputs = stringList(d["input_modalities"])
		}
		var modalities []string
		for _, s := range inputs {
			if (s == "text" || s == "image") && !includes(modalities, s) {
				modalities = append(modalities, s)
			}
		}
		if len(modalities) > 0 {
			m["input_modalities"] = modalities
			m["supports_image_detail_original"] = includes(modalities, "image")
		}
		var levels []string
		for _, s := range stringList(asMap(d["thinking"])["levels"]) {
			if allowedEfforts[s] && !includes(levels, s) {
				levels = append(levels, s)
			}
		}
		if len(levels) == 0 {
			levels = efforts(base)
			if (!exact || len(levels) == 0) && strings.EqualFold(id, "gpt-6-astra") {
				levels = []string{"low", "medium", "high", "xhigh", "max"}
			}
		}
		if len(levels) == 0 {
			levels = []string{"medium"}
		}
		entries := []any{}
		for _, s := range levels {
			entries = append(entries, map[string]any{"effort": s, "description": s + " reasoning effort"})
		}
		m["supported_reasoning_levels"] = entries
		pref := "medium"
		if strings.EqualFold(id, "gpt-5.6-sol") {
			pref = "low"
		}
		if !includes(levels, pref) {
			pref = levels[0]
		}
		m["default_reasoning_level"] = pref
		if !exact {
			m["prefer_websockets"] = false
			m["supports_search_tool"] = false
			m["web_search_tool_type"] = "text"
			m["default_service_tier"] = nil
			m["service_tiers"] = []any{}
			m["additional_speed_tiers"] = []any{}
			delete(m, "minimal_client_version")
			m["upgrade"] = nil
			m["availability_nux"] = nil
		}
		c.Models = append(c.Models, m)
	}
	// Normalize numeric types before schema validation.
	b := marshal(c)
	json.Unmarshal(b, &c)
	return c, skipped, validateCatalog(c)
}

func (a *App) fetchCatalog(p Provider) (Catalog, error) {
	fmt.Fprintln(a.out, "正在验证接口并获取模型……")
	var payload any
	if err := fetchJSON(a.client, p.URL+"/models", p.Key, &payload); err != nil {
		return Catalog{}, err
	}
	defs, err := definitions(payload)
	if err != nil {
		return Catalog{}, err
	}
	t, err := a.templates()
	if err != nil {
		return Catalog{}, err
	}
	c, skipped, err := buildCatalog(defs, t)
	if err != nil {
		return c, err
	}
	fmt.Fprintf(a.out, "已生成 %d 个模型，过滤 %d 个非目标模型。未知模型的回退元数据不保证完整能力。\n", len(c.Models), skipped)
	return c, nil
}

func readCatalog(dir string, id int) (Catalog, error) {
	var c Catalog
	b, err := os.ReadFile(catalogPath(dir, id))
	if err != nil {
		return c, errors.New("模型目录不存在，请执行 cpa sync")
	}
	if json.Unmarshal(b, &c) != nil {
		return c, errors.New("模型目录损坏，请执行 cpa sync")
	}
	return c, validateCatalog(c)
}
