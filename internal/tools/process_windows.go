//go:build windows

package tools

import "os/exec"

// setupProcess is a no-op on Windows. The child is cancelled by the default
// CommandContext behavior, which terminates the direct process.
func setupProcess(cmd *exec.Cmd) {}
