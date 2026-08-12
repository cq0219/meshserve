//go:build !windows

package engine

import (
	"os/exec"
	"syscall"
)

// applySysProcAttr 将子进程放入独立进程组（Unix 平台），便于整体终止。
func applySysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
