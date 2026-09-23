package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

const help = `CPA 模型适配器

  cpa init       初始化，创建配置 1（检测并确认迁移已有供应商）
  cpa add        输入 API 地址和密钥，自动分配新编号
  cpa status     查看当前配置和本地状态，不进行联网检测
  cpa list       列出全部配置，不显示密钥
  cpa use 2      验证并切换到配置 2
  cpa delete 2   确认后删除配置 2
  cpa sync       更新当前配置的模型列表
  cpa restart    确认后重启当前配置对应的 Linux Codex 后端
  cpa -help      查看帮助（兼容 --help、-h）

密钥以受文件权限保护的明文保存在 ~/.cpa/providers.json。
保留既有自定义供应商 ID，新配置默认使用 cpa；环境变量统一为 CPA_API_KEY。
已运行的 Codex 不会热切换，请在切换后重新启动。
配置成功后会询问是否重启 Linux 后端；默认不重启，运行中的任务可能被中断。
可用 CPA_HOME / CPA_CODEX_CONFIG 指定隔离的数据目录和配置文件。
`

type App struct {
	dir, config    string
	in             *bufio.Reader
	out            io.Writer
	client         *http.Client
	secret         func() (string, error)
	backends       backendController
	restartPending bool
	restartTimeout time.Duration
}

func (a *App) prompt(label string) (string, error) {
	fmt.Fprint(a.out, label)
	s, err := a.in.ReadString('\n')
	if err != nil {
		return "", errors.New("输入已取消或结束；需要完整输入行")
	}
	return strings.TrimSpace(s), nil
}
func (a *App) confirm(label string) (bool, error) {
	s, e := a.prompt(label + " [y/N]：")
	return strings.EqualFold(s, "y") || strings.EqualFold(s, "yes"), e
}

func (a *App) credentials(id int, initialURL, initialKey string) (Provider, error) {
	label := "API 地址（如 https://example.com/v1）："
	if initialURL != "" {
		label = "API 地址（回车保留 " + initialURL + "）："
	}
	s, e := a.prompt(label)
	if e != nil {
		return Provider{}, e
	}
	if s == "" {
		s = initialURL
	}
	u, e := normalizeURL(s)
	if e != nil {
		return Provider{}, e
	}
	if strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "http://127.0.0.1:") && !strings.HasPrefix(u, "http://localhost:") {
		ok, e := a.confirm("该地址使用明文 HTTP，密钥可能被网络观察者读取。仍要使用？")
		if e != nil {
			return Provider{}, e
		}
		if !ok {
			return Provider{}, errors.New("已取消")
		}
	}
	label = "API 密钥（输入不回显）："
	if initialKey != "" {
		label = "API 密钥（不回显，回车导入已有密钥）："
	}
	fmt.Fprint(a.out, label)
	key, e := a.secret()
	if e != nil {
		return Provider{}, e
	}
	fmt.Fprintln(a.out)
	if key == "" {
		key = initialKey
	}
	if key == "" || strings.TrimSpace(key) != key || control(key) {
		return Provider{}, errors.New("密钥不能为空、带首尾空白或包含控制字符")
	}
	return Provider{ID: id, URL: u, Key: key}, nil
}

func (a *App) chooseModel(c Catalog, config map[string]any) (string, string, error) {
	current := str(config["model"])
	var selected map[string]any
	for _, m := range c.Models {
		if str(m["slug"]) == current {
			selected = m
			break
		}
	}
	if selected == nil {
		fmt.Fprintln(a.out, "请选择默认模型（原模型未配置或目标供应商不提供）：")
		for i, m := range c.Models {
			fmt.Fprintf(a.out, "  %d. %s\n", i+1, str(m["slug"]))
		}
		s, e := a.prompt("模型编号：")
		if e != nil {
			return "", "", e
		}
		n, e := strconv.Atoi(s)
		if e != nil || n < 1 || n > len(c.Models) {
			return "", "", errors.New("模型编号无效，未切换")
		}
		selected = c.Models[n-1]
	}
	effort := ""
	if old := str(config["model_reasoning_effort"]); old != "" && !includes(efforts(selected), old) {
		fmt.Fprintf(a.out, "原推理档位 %s 不在所选模型声明的档位中，可选：%s\n", old, strings.Join(efforts(selected), " / "))
		s, e := a.prompt("新推理档位：")
		if e != nil {
			return "", "", e
		}
		if !includes(efforts(selected), s) {
			return "", "", errors.New("推理档位无效，未切换")
		}
		effort = s
	}
	return str(selected["slug"]), effort, nil
}

func (a *App) apply(s State, p Provider, c Catalog, migrate bool, extra []Change) error {
	old, cfg, e := loadConfig(a.config)
	if e != nil {
		return e
	}
	model, effort, e := a.chooseModel(c, cfg)
	if e != nil {
		return e
	}
	merged, e := mergeConfig(old, p, catalogPath(a.dir, p.ID), model, effort, migrate, s.codexProvider())
	if e != nil {
		return e
	}
	// Check again after user interaction; do not overwrite edits made while the
	// prompt was open. The app lock serializes all CPA commands.
	now, _, e := loadConfig(a.config)
	if e != nil {
		return e
	}
	if !bytes.Equal(old, now) {
		return errors.New("config.toml 在操作期间发生变化，已取消；请重试")
	}
	backup, e := backupConfig(a.dir, a.config)
	if e != nil {
		return e
	}
	s.Current = p.ID
	changes := []Change{{Path: catalogPath(a.dir, p.ID), Data: marshal(c)}, {Path: a.config, Data: merged}, {Path: filepath.Join(a.dir, "providers.json"), Data: marshal(s)}}
	changes = append(changes, extra...)
	if e = commit(a.dir, changes); e != nil {
		return e
	}
	if backup != "" {
		fmt.Fprintln(a.out, "原 Codex 配置已备份：", backup)
	}
	fmt.Fprintf(a.out, "当前配置：%d\nAPI 地址：%s\n模型：%s\n", p.ID, p.URL, model)
	if os.Getenv("CPA_SHELL_INTEGRATED") == "1" {
		fmt.Fprintln(a.out, "命令返回后终端包装层将刷新 CPA_API_KEY；远程后端需要单独重启。")
	} else {
		fmt.Fprintln(a.out, "配置已写入，但普通子进程不能修改当前终端环境。请加载安装脚本设置的终端集成，再启动 Codex；cpa status 可检查密钥是否匹配。")
	}
	a.restartPending = true
	return nil
}

func (a *App) init() error {
	if _, e := os.Stat(filepath.Join(a.dir, "providers.json")); e == nil {
		return errors.New("已经初始化；请使用 cpa add 添加配置，不会覆盖已有编号")
	}
	_, cfg, e := loadConfig(a.config)
	if e != nil {
		return e
	}
	oldID := str(cfg["model_provider"])
	p := asMap(asMap(cfg["model_providers"])[oldID])
	providerID := "cpa"
	if oldID != "" && p != nil {
		providerID = oldID
	}
	initialURL := str(p["base_url"])
	if initialURL != "" {
		if _, e = normalizeURL(initialURL); e != nil {
			initialURL = ""
		}
	}
	initialKey := ""
	if name := str(p["env_key"]); name != "" {
		initialKey = os.Getenv(name)
	}
	if len(cfg) > 0 {
		fmt.Fprintf(a.out, "检测到已有 Codex 配置。将备份并使用供应商 ID %s、密钥变量 CPA_API_KEY，接管模型目录。\n", providerID)
		fmt.Fprintln(a.out, "保留既有自定义供应商 ID；其他供应商、MCP、权限、插件和会话文件不删除。")
		fmt.Fprintln(a.out, "旧终端密钥设置暂不自动删除；已运行的 Codex/SSH 后端需要重新启动。")
	}
	fmt.Fprintln(a.out, "密钥将以明文保存在仅当前用户可访问的私密文件中（不等于加密）。")
	ok, e := a.confirm("确认初始化并应用统一配置？")
	if e != nil {
		return e
	}
	if !ok {
		return errors.New("已取消，未修改 Codex 配置")
	}
	provider, e := a.credentials(1, initialURL, initialKey)
	if e != nil {
		return e
	}
	// Validate the edit shape before contacting any API.
	if _, e = mergeConfig(mustConfigBytes(a.config), provider, catalogPath(a.dir, 1), "validation-placeholder", "", true, providerID); e != nil {
		return e
	}
	c, e := a.fetchCatalog(provider)
	if e != nil {
		return e
	}
	s := State{Version: 1, NextID: 2, Current: 1, ConfigPath: a.config, CodexProvider: providerID, Providers: []Provider{provider}}
	return a.apply(s, provider, c, true, nil)
}

func mustConfigBytes(path string) []byte { b, _ := os.ReadFile(path); return b }

func (a *App) state() (State, error) {
	s, e := readState(a.dir)
	if e != nil {
		return s, e
	}
	if filepath.Clean(s.ConfigPath) != filepath.Clean(a.config) {
		return s, errors.New("当前 Codex 配置路径与初始化时不同；请使用匹配的 CPA_HOME/CODEX_HOME，不跨配置写入")
	}
	return s, nil
}

func (a *App) add(s State) error {
	p, e := a.credentials(s.NextID, "", "")
	if e != nil {
		return e
	}
	c, e := a.fetchCatalog(p)
	if e != nil {
		return e
	}
	s.Providers = append(s.Providers, p)
	s.NextID++
	if e = commit(a.dir, []Change{{Path: catalogPath(a.dir, p.ID), Data: marshal(c)}, {Path: filepath.Join(a.dir, "providers.json"), Data: marshal(s)}}); e != nil {
		return e
	}
	fmt.Fprintf(a.out, "已添加配置 %d，当前配置仍是 %d。\n", p.ID, s.Current)
	ok, e := a.confirm("是否立即切换？")
	if e != nil {
		fmt.Fprintln(a.out, "配置已保存，未切换。可稍后执行 cpa use。 ")
		return nil
	}
	if ok {
		return a.apply(s, p, c, false, nil)
	}
	return nil
}

func parseID(s string) (int, error) {
	id, e := strconv.Atoi(s)
	if e != nil || id < 1 {
		return 0, errors.New("请输入正整数配置编号")
	}
	return id, nil
}

func (a *App) delete(s State, id int) error {
	p, e := s.provider(id)
	if e != nil {
		return e
	}
	if id == s.Current && len(s.Providers) == 1 {
		return errors.New("不能删除唯一的当前配置；请先 cpa add 添加另一项")
	}
	ok, e := a.confirm(fmt.Sprintf("删除配置 %d（%s）的地址、密钥和模型文件？", id, p.URL))
	if e != nil {
		return e
	}
	if !ok {
		return errors.New("已取消")
	}
	remaining := []Provider{}
	for _, v := range s.Providers {
		if v.ID != id {
			remaining = append(remaining, v)
		}
	}
	s.Providers = remaining
	deletion := Change{Path: catalogPath(a.dir, id), Delete: true}
	if id == s.Current {
		fmt.Fprintln(a.out, "这是当前配置，必须先成功切换到另一项：")
		for _, v := range remaining {
			fmt.Fprintf(a.out, "  %d  %s\n", v.ID, v.URL)
		}
		choice, e := a.prompt("切换到编号：")
		if e != nil {
			return e
		}
		n, e := parseID(choice)
		if e != nil {
			return e
		}
		target, e := s.provider(n)
		if e != nil {
			return e
		}
		c, e := a.fetchCatalog(target)
		if e != nil {
			return e
		}
		if e = a.apply(s, target, c, false, []Change{deletion}); e != nil {
			return e
		}
	} else {
		if e = commit(a.dir, []Change{{Path: filepath.Join(a.dir, "providers.json"), Data: marshal(s)}, deletion}); e != nil {
			return e
		}
	}
	fmt.Fprintf(a.out, "已删除配置 %d；其他编号保持不变，不复用已删除编号。删除不能保证对磁盘数据进行安全擦除。\n", id)
	return nil
}

func (a *App) status(s State) error {
	p, _ := s.provider(s.Current)
	_, cfg, e := loadConfig(a.config)
	if e != nil {
		return e
	}
	fmt.Fprintf(a.out, "当前配置：%d\nAPI 地址：%s\n当前模型：%s\nCodex 配置：%s\n", p.ID, p.URL, str(cfg["model"]), a.config)
	fmt.Fprintln(a.out, "Codex 供应商 ID：", s.codexProvider())
	provider := asMap(asMap(cfg["model_providers"])[s.codexProvider()])
	match := str(cfg["model_provider"]) == s.codexProvider() && str(provider["base_url"]) == p.URL && str(provider["env_key"]) == "CPA_API_KEY" && str(cfg["model_catalog_json"]) == catalogPath(a.dir, p.ID)
	if match {
		fmt.Fprintln(a.out, "配置引用：匹配")
	} else {
		fmt.Fprintln(a.out, "配置引用：不匹配（可能有外部修改）")
	}
	if c, e := readCatalog(a.dir, p.ID); e == nil {
		fmt.Fprintf(a.out, "模型目录：已生成（%d 个）\n", len(c.Models))
	} else {
		fmt.Fprintln(a.out, "模型目录：缺失或无效")
	}
	fmt.Fprintln(a.out, "密钥文件：已配置（不显示密钥）")
	if os.Getenv("CPA_API_KEY") == p.Key {
		fmt.Fprintln(a.out, "当前进程环境：密钥匹配")
	} else {
		fmt.Fprintln(a.out, "当前进程环境：密钥缺失或不匹配；请加载终端集成/重新打开终端")
	}
	fmt.Fprintln(a.out, "以上仅检查当前进程，不能证明已运行的 Codex/SSH 后端获得密钥；修改后请重启后端。未执行联网检测。")
	return nil
}

func (a *App) run(args []string) error {
	a.restartPending = false
	if len(args) == 1 && args[0] == "restart" {
		return a.restartBackends()
	}
	if err := a.runLocked(args); err != nil {
		return err
	}
	if a.restartPending {
		if err := a.restartBackends(); err != nil {
			fmt.Fprintln(a.out, "配置已保存，但后端重启未完成：", err)
			fmt.Fprintln(a.out, "不要重新初始化；可稍后在外部终端执行 cpa restart。")
		}
	}
	return nil
}

func (a *App) runLocked(args []string) error {
	if len(args) == 0 {
		return errors.New("请指定命令，使用 cpa -help 查看帮助")
	}
	if len(args) == 1 && (args[0] == "-help" || args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(a.out, help)
		return nil
	}
	if len(args) == 1 && args[0] == "_setup" {
		return a.setup()
	}
	cmd := args[0]
	n := 1
	if cmd == "use" || cmd == "delete" {
		n = 2
	}
	if len(args) != n {
		return errors.New("命令参数不正确，使用 cpa -help 查看帮助")
	}
	mutation := cmd == "init" || cmd == "add" || cmd == "use" || cmd == "delete" || cmd == "sync"
	if !mutation && cmd != "list" && cmd != "status" && cmd != "_key" {
		return errors.New("未知命令，使用 cpa -help 查看帮助")
	}
	if mutation {
		if e := ensurePrivateDir(a.dir); e != nil {
			return e
		}
	} else {
		if _, e := os.Stat(a.dir); e != nil {
			return errors.New("尚未初始化，请执行 cpa init")
		}
	}
	unlock, e := lockState(filepath.Join(a.dir, ".lock"))
	if e != nil {
		return e
	}
	defer unlock()
	if mutation {
		if e = recoverTransaction(a.dir); e != nil {
			return e
		}
	} else if _, e = os.Stat(filepath.Join(a.dir, "transaction.json")); e == nil {
		return errors.New("检测到未完成事务；请执行修改命令以自动恢复，暂不读取混合状态")
	}
	if cmd == "init" {
		return a.init()
	}
	s, e := a.state()
	if e != nil {
		return e
	}
	switch cmd {
	case "_key":
		if os.Getenv("CPA_SHELL_INTEGRATED") != "1" || term.IsTerminal(int(os.Stdout.Fd())) {
			return errors.New("内部凭据接口仅供终端集成通过管道使用")
		}
		p, _ := s.provider(s.Current)
		_, cfg, err := loadConfig(a.config)
		if err != nil {
			return err
		}
		provider := asMap(asMap(cfg["model_providers"])[s.codexProvider()])
		if str(cfg["model_provider"]) != s.codexProvider() || str(provider["base_url"]) != p.URL || str(provider["env_key"]) != "CPA_API_KEY" {
			return errors.New("配置引用已改变，拒绝为不匹配的地址导出密钥")
		}
		fmt.Fprint(a.out, p.Key)
		return nil
	case "list":
		fmt.Fprintln(a.out, "编号  API 地址  当前")
		for _, p := range s.Providers {
			mark := ""
			if p.ID == s.Current {
				mark = "*"
			}
			fmt.Fprintf(a.out, "%d  %s  %s\n", p.ID, p.URL, mark)
		}
		return nil
	case "status":
		return a.status(s)
	case "add":
		return a.add(s)
	case "delete":
		id, e := parseID(args[1])
		if e != nil {
			return e
		}
		return a.delete(s, id)
	case "use", "sync":
		id := s.Current
		if cmd == "use" {
			id, e = parseID(args[1])
			if e != nil {
				return e
			}
		}
		p, e := s.provider(id)
		if e != nil {
			return e
		}
		c, e := a.fetchCatalog(p)
		if e != nil {
			return e
		}
		return a.apply(s, p, c, false, nil)
	}
	return nil
}

func main() {
	dir, config, err := defaultPaths()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cpa: 无法确定用户配置目录")
		os.Exit(1)
	}
	a := &App{dir: dir, config: config, in: bufio.NewReader(os.Stdin), out: os.Stdout, client: httpClient()}
	a.secret = func() (string, error) {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			b, e := term.ReadPassword(int(os.Stdin.Fd()))
			if e != nil {
				return "", errors.New("无法安全读取密钥")
			}
			return string(b), nil
		}
		return "", errors.New("密钥输入需要交互终端，禁止通过命令参数或普通管道提供密钥")
	}
	if err = a.run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cpa:", err)
		os.Exit(1)
	}
}
