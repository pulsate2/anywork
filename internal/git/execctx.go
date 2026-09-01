package git

import (
	"context"
	"os"
	"time"
)

// commandCtx 给 git 命令一个合理超时,避免远端操作(hang)拖死请求。
type commandCtx struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func newCommandContext() *commandCtx {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	return &commandCtx{ctx: ctx, cancel: cancel}
}

// mergedEnv 合并系统环境与追加变量,确保 git 可找到 ssh-agent、凭据等。
func mergedEnv(extra []string) []string {
	if len(extra) == 0 {
		return os.Environ()
	}
	return append(os.Environ(), extra...)
}
