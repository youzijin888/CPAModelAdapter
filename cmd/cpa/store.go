package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

type Provider struct {
	ID  int    `json:"id"`
	URL string `json:"url"`
	Key string `json:"key"`
}

type State struct {
	Version       int        `json:"version"`
	NextID        int        `json:"next_id"`
	Current       int        `json:"current"`
	ConfigPath    string     `json:"config_path"`
	CodexProvider string     `json:"codex_provider,omitempty"`
	Providers     []Provider `json:"providers"`
}

func (s State) codexProvider() string {
	if s.CodexProvider == "" {
		return "cpa"
	}
	return s.CodexProvider
}

func (s State) provider(id int) (Provider, error) {
	for _, p := range s.Providers {
		if p.ID == id {
			return p, nil
		}
	}
	return Provider{}, fmt.Errorf("配置 %d 不存在；使用 cpa list 查看编号", id)
}

func readState(dir string) (State, error) {
	var s State
	b, err := os.ReadFile(filepath.Join(dir, "providers.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, errors.New("尚未初始化，请执行 cpa init")
	}
	if err != nil {
		return s, errors.New("无法读取私密配置文件")
	}
	if json.Unmarshal(b, &s) != nil || s.Version != 1 || s.NextID < 2 || s.ConfigPath == "" {
		return s, errors.New("私密配置文件无效；未执行任何覆盖")
	}
	seen := map[int]bool{}
	for _, p := range s.Providers {
		if p.ID < 1 || seen[p.ID] || p.ID >= s.NextID || p.Key == "" {
			return s, errors.New("私密配置中的编号或密钥无效")
		}
		if _, err := normalizeURL(p.URL); err != nil {
			return s, errors.New("私密配置中的接口地址无效")
		}
		seen[p.ID] = true
	}
	if !seen[s.Current] {
		return s, errors.New("当前配置编号无效")
	}
	return s, nil
}

func marshal(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("私密目录不能是符号链接或普通文件")
	}
	return protect(path, true)
}

func atomicWrite(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("拒绝覆盖符号链接或非普通文件")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".cpa-write-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	if err = protect(tmp, false); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type Change struct {
	Path   string `json:"path"`
	Data   []byte `json:"data,omitempty"`
	Delete bool   `json:"delete,omitempty"`
}

type Journal struct {
	Originals []Change `json:"originals"`
}

func applyChange(c Change) error {
	if c.Delete {
		err := os.Remove(c.Path)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return nil
		}
		return err
	}
	return atomicWrite(c.Path, c.Data)
}

// A private, durable undo journal makes interrupted multi-file switches recoverable.
// The journal contains credentials; it never goes to stdout or persistent backups.
func commit(dir string, changes []Change) error {
	j := Journal{}
	seen := map[string]bool{}
	for _, c := range changes {
		if seen[c.Path] {
			return errors.New("事务中存在重复目标")
		}
		seen[c.Path] = true
		if info, e := os.Lstat(c.Path); e == nil && !info.Mode().IsRegular() {
			return errors.New("事务目标不是普通文件，拒绝覆盖")
		}
		b, e := os.ReadFile(c.Path)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		j.Originals = append(j.Originals, Change{Path: c.Path, Data: b, Delete: errors.Is(e, os.ErrNotExist)})
	}
	path := filepath.Join(dir, "transaction.json")
	if err := atomicWrite(path, marshal(j)); err != nil {
		return err
	}
	for _, c := range changes {
		if err := applyChange(c); err != nil {
			if rerr := recoverTransaction(dir); rerr != nil {
				return fmt.Errorf("写入失败且恢复未完成；请保留私密目录，下次修改命令会重试恢复：%w", rerr)
			}
			return fmt.Errorf("写入失败，已恢复原配置：%w", err)
		}
	}
	return os.Remove(path)
}

func recoverTransaction(dir string) error {
	path := filepath.Join(dir, "transaction.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var j Journal
	if json.Unmarshal(b, &j) != nil || len(j.Originals) == 0 {
		return errors.New("恢复记录损坏，拒绝继续修改")
	}
	var failures []error
	for i := len(j.Originals) - 1; i >= 0; i-- {
		if err := applyChange(j.Originals[i]); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	return os.Remove(path)
}

func backupConfig(dir, config string) (string, error) {
	b, err := os.ReadFile(config)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "backups", time.Now().UTC().Format("20060102T150405.000000000Z")+"-config.toml")
	return path, atomicWrite(path, b)
}

func catalogPath(dir string, id int) string {
	return filepath.Join(dir, "models", strconv.Itoa(id)+".json")
}
