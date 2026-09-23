//go:build !linux

package main

import "errors"

func newBackendController() (backendController, error) {
	return nil, errors.New("自动重启目前仅支持 Linux SSH 后端；本平台请手动退出并重新启动 Codex")
}
