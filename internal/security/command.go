package security

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CommandSpec is the runtime-enforced description of a process launch.
type CommandSpec struct {
	Executable string
	Args       []string
	// Dir is a workspace-relative or absolute working directory. Empty means
	// the workspace root.
	Dir string
	// Shell marks a request that will be interpreted by a shell.
	Shell bool
}

// CheckCommand authorizes a process launch and returns the resolved working
// directory. It never claims to sandbox the child process; it only enforces
// policy before execution.
func (p *Policy) CheckCommand(cmd CommandSpec) (string, error) {
	if p == nil || p.workspace == "" {
		return "", fmt.Errorf("%w: policy is not initialized", ErrDenied)
	}
	if !p.AllowExecute {
		return "", fmt.Errorf("%w: command execution is disabled", ErrDenied)
	}
	exe := strings.TrimSpace(cmd.Executable)
	if exe == "" {
		return "", fmt.Errorf("%w: executable is required", ErrInvalidPath)
	}
	if strings.IndexByte(exe, 0) >= 0 {
		return "", fmt.Errorf("%w: executable contains NUL byte", ErrInvalidPath)
	}
	if (cmd.Shell || IsShellExecutable(exe)) && !p.AllowShell {
		return "", fmt.Errorf("%w: shell execution is disabled", ErrDenied)
	}
	if IsNetworkCommand(exe, cmd.Args) && !p.AllowNetwork {
		return "", fmt.Errorf("%w: network access is disabled", ErrDenied)
	}
	dir := p.workspace
	if strings.TrimSpace(cmd.Dir) != "" {
		resolved, err := p.Resolve(cmd.Dir)
		if err != nil {
			return "", err
		}
		if _, err := p.ensurePhysical(resolved, false); err != nil {
			return "", err
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return "", fmt.Errorf("security: stat command directory %q: %w", cmd.Dir, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%w: command directory %q is not a directory", ErrInvalidPath, cmd.Dir)
		}
		dir = resolved
	}
	return dir, nil
}

// IsShellExecutable reports whether the executable is a known command shell.
func IsShellExecutable(exe string) bool {
	name := strings.ToLower(filepath.Base(strings.TrimSpace(exe)))
	name = strings.TrimSuffix(name, ".exe")
	switch name {
	case "sh", "bash", "zsh", "dash", "fish", "cmd", "powershell", "pwsh", "command", "ksh":
		return true
	default:
		return false
	}
}

// IsDestructive is a heuristic used for approval UX and labeling. It is not a
// security boundary.
func IsDestructive(exe string, args []string) bool {
	name := strings.ToLower(filepath.Base(strings.TrimSpace(exe)))
	name = strings.TrimSuffix(name, ".exe")
	switch name {
	case "rm", "rmdir", "rd", "del", "erase", "format", "mkfs", "dd", "shutdown", "reboot", "taskkill", "diskpart", "reg":
		return true
	case "remove-item", "clear-disk", "format-volume":
		return true
	}
	if name == "git" {
		joined := strings.ToLower(strings.Join(args, " "))
		for _, pattern := range []string{
			"reset --hard", "clean -fd", "clean -f", "checkout --", "restore .", "push --force", "push -f", "branch -d", "branch -d",
		} {
			if strings.Contains(joined, pattern) {
				return true
			}
		}
	}
	return false
}

// IsNetworkCommand is a heuristic for labeling and policy checks. It is not a
// complete network sandbox.
func IsNetworkCommand(exe string, args []string) bool {
	name := strings.ToLower(filepath.Base(strings.TrimSpace(exe)))
	name = strings.TrimSuffix(name, ".exe")
	switch name {
	case "curl", "wget", "ssh", "scp", "sftp", "telnet", "nc", "ncat", "ftp", "ping":
		return true
	}
	if name == "git" {
		// Scan every argument rather than only args[0]: global flags such as
		// `git -C dir fetch` put the subcommand later. False positives only
		// make the policy stricter.
		for _, arg := range args {
			switch strings.ToLower(arg) {
			case "clone", "fetch", "pull", "push", "ls-remote", "remote", "submodule":
				return true
			}
		}
	}
	if (name == "go" && hasArg(args, "get")) ||
		(name == "npm" && hasArg(args, "install")) ||
		(name == "pip" && hasArg(args, "install")) {
		return true
	}
	return false
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if strings.EqualFold(arg, want) {
			return true
		}
	}
	return false
}
