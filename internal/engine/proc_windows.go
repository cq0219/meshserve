//go:build windows

package engine

import "os/exec"

// applySysProcAttr Windows 平台无进程组语义，无需设置。
func applySysProcAttr(cmd *exec.Cmd) {}
