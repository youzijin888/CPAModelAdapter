package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type backendProcess struct {
	PID        int
	Start      string
	Home       string
	KeyMatches bool
	Ancestor   bool
}

type backendController interface {
	list(configPath, key string) ([]backendProcess, error)
	stop(backendProcess) error
}

func (app *App) restartTarget() (string, [32]byte, error) {
	unlock, err := lockState(filepath.Join(app.dir, ".lock"))
	if err != nil {
		return "", [32]byte{}, err
	}
	defer unlock()
	if _, err = os.Stat(filepath.Join(app.dir, "transaction.json")); !os.IsNotExist(err) {
		return "", [32]byte{}, errors.New("存在未完成事务或无法检查事务状态，拒绝重启")
	}
	state, err := app.state()
	if err != nil {
		return "", [32]byte{}, err
	}
	provider, err := state.provider(state.Current)
	if err != nil {
		return "", [32]byte{}, err
	}
	raw, configuration, err := loadConfig(app.config)
	if err != nil {
		return "", [32]byte{}, err
	}
	configured := asMap(asMap(configuration["model_providers"])[state.codexProvider()])
	if str(configuration["model_provider"]) != state.codexProvider() || str(configured["env_key"]) != "CPA_API_KEY" || str(configured["base_url"]) != provider.URL {
		return "", [32]byte{}, errors.New("Codex 配置与 CPA 状态不匹配，拒绝重启")
	}
	return provider.Key, sha256.Sum256(append(marshal(state), raw...)), nil
}

func (app *App) restartBackends() error {
	key, fingerprint, err := app.restartTarget()
	if err != nil {
		return err
	}
	controller := app.backends
	if controller == nil {
		controller, err = newBackendController()
		if err != nil {
			return err
		}
	}
	unlock, err := lockState(filepath.Join(app.dir, ".restart.lock"))
	if err != nil {
		return errors.New("另一个后端重启流程正在运行，请稍后重试")
	}
	defer unlock()
	processes, err := controller.list(app.config, key)
	if err != nil {
		return err
	}
	if len(processes) == 0 {
		fmt.Fprintln(app.out, "未发现当前用户、当前 Codex 配置对应的受支持 Linux 后端；没有停止任何进程。")
		return nil
	}
	for _, process := range processes {
		if process.Ancestor {
			return errors.New("当前命令运行在待重启的 Codex 后端内部；请在独立 SSH 终端执行 cpa restart，避免中断自身任务")
		}
	}
	fmt.Fprintf(app.out, "检测到 %d 个同用户、同 CODEX_HOME 的 Codex 后端，PID：", len(processes))
	for _, process := range processes {
		fmt.Fprintf(app.out, " %d", process.PID)
	}
	fmt.Fprintln(app.out, "\n重启会短暂断开连接，并可能中断这些后端中的运行任务。不能仅凭进程信息确认任务空闲。")
	confirmed, err := app.confirm("确认现在重启远程 Codex 后端？")
	if err != nil || !confirmed {
		fmt.Fprintln(app.out, "未重启后端；可稍后在独立 SSH 终端执行 cpa restart。")
		return nil
	}
	_, currentFingerprint, err := app.restartTarget()
	if err != nil {
		return err
	}
	if currentFingerprint != fingerprint {
		return errors.New("确认期间 CPA/Codex 配置发生变化，未停止任何进程；请重新执行 cpa restart")
	}
	for _, process := range processes {
		if err = controller.stop(process); err != nil {
			return fmt.Errorf("后端 PID %d 未能正常退出；未强制终止其他进程：%w", process.PID, err)
		}
	}
	fmt.Fprintln(app.out, "已向旧后端发送 SIGTERM，等待桌面端自动重连；不会使用 SIGKILL，也不会创建无客户端的替代后端。")
	timeout := app.restartTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		current, err := controller.list(app.config, key)
		if err != nil {
			return err
		}
		ready := len(current) >= len(processes)
		for _, candidate := range current {
			if !candidate.KeyMatches {
				ready = false
			}
			for _, old := range processes {
				if old.PID == candidate.PID && old.Start == candidate.Start {
					ready = false
				}
			}
		}
		if ready {
			fmt.Fprintln(app.out, "后端重启已验证：旧进程已退出，新后端已启动，CPA_API_KEY 与当前配置匹配（未显示密钥）。此检查不代表模型接口调用成功。")
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("未能在等待时间内确认新后端及其密钥；请在桌面端重新连接。配置已经保存，不要重新初始化")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
