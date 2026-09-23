package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

func configData(data []byte) (map[string]any, error) {
	c := map[string]any{}
	if err := toml.Unmarshal(data, &c); err != nil {
		return nil, errors.New("config.toml 解析失败；为防止泄露密钥，不打印文件内容")
	}
	return c, nil
}

func loadConfig(path string) ([]byte, map[string]any, error) {
	b, e := os.ReadFile(path)
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, nil, e
	}
	c, e := configData(b)
	return b, c, e
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func str(v any) string           { s, _ := v.(string); return s }

func tomlString(v string) string {
	// Go's TOML encoder handles Windows paths and control characters correctly.
	b, _ := toml.Marshal(map[string]string{"v": v})
	return strings.TrimSpace(strings.SplitN(string(b), "=", 2)[1])
}

type edit struct {
	start, end int
	text       string
}
type block struct {
	path       []string
	start, end int
}

func nodeKey(n *unstable.Node) []string {
	var keys []string
	it := n.Key()
	for it.Next() {
		keys = append(keys, string(it.Node().Data))
	}
	return keys
}
func lineStart(b []byte, offset int) int { return bytes.LastIndexByte(b[:offset], '\n') + 1 }

func referencedProvider(c map[string]any, id string) bool {
	for _, v := range asMap(c["profiles"]) {
		if str(asMap(v)["model_provider"]) == id {
			return true
		}
	}
	return false
}

// Patch only root selector values and explicitly owned provider sections. All
// other bytes (including comments, multiline strings and CRLF) are preserved.
// Valid TOML forms that cannot be safely spliced are rejected, never reserialized.
func mergeConfig(old []byte, p Provider, catalog, model, effort string, migrate bool, providerID string) ([]byte, error) {
	c, err := configData(old)
	if err != nil {
		return nil, err
	}
	if str(c["profile"]) != "" {
		return nil, errors.New("检测到默认 profile；请先取消其供应商覆盖，CPA 不会擅自修改 profile")
	}
	oldID := str(c["model_provider"])
	providers := asMap(c["model_providers"])
	if providerID == "" || control(providerID) {
		return nil, errors.New("Codex 供应商 ID 无效")
	}
	if !migrate && oldID != providerID {
		return nil, errors.New("Codex 当前供应商已被其他工具修改；请先恢复 CPA 配置，不覆盖外部改动")
	}
	if migrate && oldID != providerID && providers[providerID] != nil {
		return nil, errors.New("目标供应商 ID 已被占用；请先处理名称冲突")
	}
	if migrate && referencedProvider(c, providerID) {
		return nil, errors.New("当前供应商被其他 profile 引用；为保留其认证配置，请先解除共享引用")
	}
	remove := map[string]bool{providerID: true}
	providerName := str(asMap(providers[providerID])["name"])
	if providerName == "" {
		providerName = "CPA"
	}
	values := map[string]string{"model_provider": providerID, "model_catalog_json": catalog, "model": model}
	if effort != "" {
		values["model_reasoning_effort"] = effort
	}
	var edits []edit
	var blocks []block
	var table []string
	seen := map[string]bool{}
	var parser unstable.Parser
	parser.Reset(old)
	for parser.NextExpression() {
		n := parser.Expression()
		if n.Kind == unstable.Table || n.Kind == unstable.ArrayTable {
			keys := nodeKey(n)
			it := n.Key()
			it.Next()
			start := lineStart(old, int(it.Node().Raw.Offset))
			if len(blocks) > 0 {
				blocks[len(blocks)-1].end = start
			}
			blocks = append(blocks, block{keys, start, len(old)})
			table = keys
		} else if n.Kind == unstable.KeyValue {
			keys := nodeKey(n)
			full := append(append([]string{}, table...), keys...)
			// Inline/dotted provider definitions need a different editor. Refuse
			// instead of flattening tables and damaging unrelated configuration.
			if len(full) > 0 && full[0] == "model_providers" && (len(table) < 2) {
				return nil, errors.New("供应商使用了内联或点号赋值；请改为 [model_providers.名称] 表后再迁移，原文件未修改")
			}
			if len(table) == 0 && len(keys) == 1 {
				if v, ok := values[keys[0]]; ok {
					r := n.Value().Raw
					if n.Value().Kind != unstable.String || r.Length == 0 {
						return nil, errors.New("模型选择字段不是字符串，拒绝修改")
					}
					edits = append(edits, edit{int(r.Offset), int(r.Offset + r.Length), tomlString(v)})
					seen[keys[0]] = true
				}
			}
		}
	}
	if parser.Error() != nil {
		return nil, errors.New("无法安全定位 TOML 配置字段")
	}
	for _, b := range blocks {
		if len(b.path) >= 2 && b.path[0] == "model_providers" && remove[b.path[1]] {
			edits = append(edits, edit{b.start, b.end, ""})
		}
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	result := append([]byte{}, old...)
	for _, e := range edits {
		result = append(append(append([]byte{}, result[:e.start]...), []byte(e.text)...), result[e.end:]...)
	}
	nl := "\n"
	if bytes.Contains(old, []byte("\r\n")) {
		nl = "\r\n"
	}
	prefix := ""
	for _, k := range []string{"model_provider", "model_catalog_json", "model", "model_reasoning_effort"} {
		if v, ok := values[k]; ok && !seen[k] {
			prefix += k + " = " + tomlString(v) + nl
		}
	}
	result = append([]byte(prefix), result...)
	section := nl + "[model_providers." + tomlString(providerID) + "]" + nl + "name = " + tomlString(providerName) + nl + "base_url = " + tomlString(p.URL) + nl + "env_key = \"CPA_API_KEY\"" + nl + "wire_api = \"responses\"" + nl
	result = append(result, []byte(section)...)
	updated, err := configData(result)
	if err != nil {
		return nil, err
	}
	// Semantic guard in addition to byte-preserving editing: no unrelated key
	// may be altered, even for unusual TOML table arrangements.
	before, _ := configData(old)
	after, _ := configData(result)
	for k := range values {
		delete(before, k)
		delete(after, k)
	}
	for _, data := range []map[string]any{before, after} {
		ps := asMap(data["model_providers"])
		for id := range remove {
			delete(ps, id)
		}
		if len(ps) == 0 {
			delete(data, "model_providers")
		}
	}
	if !reflect.DeepEqual(before, after) || str(updated["model_provider"]) != providerID {
		return nil, errors.New("配置保留校验失败，原文件未修改")
	}
	return result, nil
}

func defaultPaths() (string, string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	dir := os.Getenv("CPA_HOME")
	if dir == "" {
		dir = filepath.Join(h, ".cpa")
	}
	ch := os.Getenv("CODEX_HOME")
	if ch == "" {
		ch = filepath.Join(h, ".codex")
	}
	config := os.Getenv("CPA_CODEX_CONFIG")
	if config == "" {
		config = filepath.Join(ch, "config.toml")
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	config, err = filepath.Abs(config)
	return dir, config, err
}
