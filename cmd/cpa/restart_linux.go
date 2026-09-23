//go:build linux

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type linuxBackends struct {
	root string
	uid  uint32
	self int
}

func newBackendController() (backendController, error) {
	return &linuxBackends{root: "/proc", uid: uint32(os.Getuid()), self: os.Getpid()}, nil
}

func processIdentity(raw []byte) (int, string, error) {
	end := bytes.LastIndex(raw, []byte(") "))
	if end < 0 {
		return 0, "", errors.New("无法识别进程身份")
	}
	fields := strings.Fields(string(raw[end+2:]))
	if len(fields) < 20 {
		return 0, "", errors.New("进程身份信息不完整")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, "", errors.New("进程父级无效")
	}
	if _, err = strconv.ParseUint(fields[19], 10, 64); err != nil {
		return 0, "", errors.New("进程启动时间无效")
	}
	return parent, fields[19], nil
}

func (controller *linuxBackends) ancestors() (map[int]bool, error) {
	ancestors := map[int]bool{}
	current := controller.self
	for current > 1 {
		if ancestors[current] || len(ancestors) >= 256 {
			return nil, errors.New("无法安全识别当前进程的祖先")
		}
		ancestors[current] = true
		raw, err := os.ReadFile(filepath.Join(controller.root, strconv.Itoa(current), "stat"))
		if err != nil {
			return nil, errors.New("无法读取当前进程祖先；为避免中断自身任务，拒绝重启")
		}
		current, _, err = processIdentity(raw)
		if err != nil {
			return nil, err
		}
	}
	return ancestors, nil
}

func supportedAppServer(arguments []byte) bool {
	values := bytes.Split(bytes.TrimRight(arguments, "\x00"), []byte{0})
	found := false
	for index := 1; index < len(values); index++ {
		argument := string(values[index])
		if argument == "app-server" {
			found = true
			continue
		}
		if argument == "--profile" || argument == "-p" || strings.HasPrefix(argument, "--profile=") {
			return false
		}
		if argument == "-c" || argument == "--config" {
			index++
			if index >= len(values) || !strings.HasPrefix(string(values[index]), "features.") || strings.ContainsAny(string(values[index]), "\r\n") {
				return false
			}
		} else if strings.HasPrefix(argument, "--config=") || strings.HasPrefix(argument, "-c") && len(argument) > 2 {
			return false
		} else if !found {
			return false
		}
	}
	return found
}

func (controller *linuxBackends) inspect(pid int, home, key string) (backendProcess, bool, error) {
	process := backendProcess{PID: pid, Home: home}
	directory := filepath.Join(controller.root, strconv.Itoa(pid))
	var metadata unix.Stat_t
	if err := unix.Stat(directory, &metadata); err != nil {
		return process, false, err
	}
	if metadata.Uid != controller.uid {
		return process, false, nil
	}
	executable, err := os.Readlink(filepath.Join(directory, "exe"))
	if err != nil {
		return process, false, err
	}
	if filepath.Base(strings.TrimSuffix(executable, " (deleted)")) != "codex" {
		return process, false, nil
	}
	arguments, err := os.ReadFile(filepath.Join(directory, "cmdline"))
	if err != nil {
		return process, false, err
	}
	if !supportedAppServer(arguments) {
		return process, false, nil
	}
	raw, err := os.ReadFile(filepath.Join(directory, "environ"))
	if err != nil {
		return process, false, errors.New("无法检查候选 Codex 后端环境，拒绝猜测其配置归属")
	}
	environment := map[string]string{}
	for _, entry := range bytes.Split(raw, []byte{0}) {
		name, value, found := strings.Cut(string(entry), "=")
		if found && (name == "HOME" || name == "CODEX_HOME" || name == "CPA_API_KEY") {
			environment[name] = value
		}
	}
	processHome := environment["CODEX_HOME"]
	if processHome == "" {
		if !filepath.IsAbs(environment["HOME"]) {
			return process, false, nil
		}
		processHome = filepath.Join(environment["HOME"], ".codex")
	}
	if !filepath.IsAbs(processHome) {
		return process, false, nil
	}
	processHome, err = filepath.EvalSymlinks(processHome)
	if err != nil || processHome != home {
		return process, false, nil
	}
	raw, err = os.ReadFile(filepath.Join(directory, "stat"))
	if err != nil {
		return process, false, err
	}
	_, process.Start, err = processIdentity(raw)
	process.KeyMatches = key != "" && environment["CPA_API_KEY"] == key
	return process, err == nil, err
}

func (controller *linuxBackends) list(configPath, key string) ([]backendProcess, error) {
	if filepath.Base(configPath) != "config.toml" {
		return nil, errors.New("自定义配置文件名无法与运行中的后端安全关联，请手动重启")
	}
	home, err := filepath.EvalSymlinks(filepath.Dir(configPath))
	if err != nil {
		return nil, errors.New("无法确定 Codex 配置目录")
	}
	ancestors, err := controller.ancestors()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(controller.root)
	if err != nil {
		return nil, errors.New("无法读取 Linux 进程目录")
	}
	var result []backendProcess
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		process, matched, err := controller.inspect(pid, home, key)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) || errors.Is(err, os.ErrPermission) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if matched {
			process.Ancestor = ancestors[pid]
			result = append(result, process)
		}
	}
	return result, nil
}

func (controller *linuxBackends) stop(expected backendProcess) error {
	if controller.root != "/proc" {
		return errors.New("非真实进程目录，拒绝发送信号")
	}
	ancestors, err := controller.ancestors()
	if err != nil || ancestors[expected.PID] {
		return errors.New("不能重启当前命令所属的后端")
	}
	descriptor, err := unix.PidfdOpen(expected.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return errors.New("无法安全锁定进程身份（需要 Linux pidfd 支持）；未发送信号")
	}
	defer unix.Close(descriptor)
	current, matched, err := controller.inspect(expected.PID, expected.Home, "")
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil || !matched || current.Start != expected.Start {
		return errors.New("后端身份发生变化，未发送信号")
	}
	if err = unix.PidfdSendSignal(descriptor, unix.SIGTERM, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return errors.New("无法发送正常退出信号")
	}
	return nil
}
