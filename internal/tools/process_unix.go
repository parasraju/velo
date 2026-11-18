//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// setupProcess places the child in its own process group so that a timeout
// cancels the whole tree rather than only the direct child.
func setupProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pgid, err := syscall.Getpgid(cmd.Process.Pid)
		if err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			return nil
		}
		return cmd.Process.Kill()
	}
}
