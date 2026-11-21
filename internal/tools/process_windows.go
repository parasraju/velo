//go:build windows

package tools

import "os/exec"

// setupProcess is a no-op on Windows. Cancellation uses CommandContext, which
// terminates only the direct child. Descendants are not killed: Windows has
// no portable process-group signal, and a Job Object is not attached here.
// Tree-kill is best-effort and not provided. run_command sets Cmd.WaitDelay so
// a grandchild that inherited the output pipes cannot wedge Wait after the
// direct child is gone.
func setupProcess(cmd *exec.Cmd) {}
