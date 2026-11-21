package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"velo/internal/events"
	"velo/internal/security"
	"velo/internal/tools"
)

// buildApprover returns the approval decision function used by the tool
// dispatcher. An explicit Options.Approver always wins (used by tests and
// embedders).
func (a *App) buildApprover() tools.Approver {
	if a.opts.Approver != nil {
		return a.opts.Approver
	}
	return a.approveTool
}

func (a *App) approveTool(ctx context.Context, def tools.Definition, call tools.Call) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mode := strings.ToLower(strings.TrimSpace(a.cfg.Security.DefaultMode))
	if mode == "" {
		mode = "ask"
	}
	destructive := isDestructiveCall(def, call)

	switch mode {
	case "deny":
		return fmt.Errorf("denied by security.default_mode=deny")
	case "auto":
		if destructive && a.cfg.Security.RequireApprovalForDestructiveCommands {
			return a.promptApproval(ctx, def, call)
		}
		return nil
	}

	if a.isTrusted(def, call) {
		return nil
	}
	return a.promptApproval(ctx, def, call)
}

func (a *App) promptApproval(ctx context.Context, def tools.Definition, call tools.Call) error {
	if !a.interactiveStdin() {
		return fmt.Errorf("approval required for %s but stdin is not a terminal; run interactively or set security.default_mode=auto", def.Name)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	destructive := isDestructiveCall(def, call)
	if a.sink != nil {
		a.emit(events.Event{Kind: events.ApprovalRequested, Tool: &events.Tool{
			ID:          call.ID,
			Name:        string(def.Name),
			Category:    string(def.Category),
			Arguments:   events.Cap(string(call.Arguments), events.MaxArgumentPreview),
			Status:      "requested",
			Destructive: destructive,
		}})
	}
	resolve := func(status, errStr string) {
		if a.sink == nil {
			return
		}
		a.emit(events.Event{Kind: events.ApprovalResolved, Tool: &events.Tool{
			ID:          call.ID,
			Name:        string(def.Name),
			Category:    string(def.Category),
			Status:      status,
			Error:       errStr,
			Destructive: destructive,
		}})
	}
	fmt.Fprintf(a.stderr, "\napproval required: %s (%s)\n", def.Name, def.Category)
	fmt.Fprintf(a.stderr, "  action: %s\n", describeCall(def, call))
	fmt.Fprintf(a.stderr, "  workspace: %s\n", a.workspace)
	if destructive {
		fmt.Fprintln(a.stderr, "  warning: this action is classified as destructive")
	}
	fmt.Fprint(a.stderr, "approve? [y]es once / [n]o / [t]rust this action for this session: ")

	line, err := a.reader.ReadString('\n')
	if err != nil && err != io.EOF {
		resolve("denied", "read approval: "+err.Error())
		return fmt.Errorf("read approval: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	switch answer {
	case "y", "yes":
		resolve("approved", "")
		return nil
	case "t", "trust":
		a.trust(def, call)
		resolve("trusted", "")
		return nil
	default:
		resolve("denied", "denied by user")
		return fmt.Errorf("denied by user")
	}
}

func (a *App) interactiveStdin() bool {
	f, ok := a.stdin.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func (a *App) trust(def tools.Definition, call tools.Call) {
	key := approvalKey(def, call)
	a.trustMu.Lock()
	defer a.trustMu.Unlock()
	if a.trusted == nil {
		a.trusted = make(map[string]struct{})
	}
	a.trusted[key] = struct{}{}
}

func (a *App) isTrusted(def tools.Definition, call tools.Call) bool {
	key := approvalKey(def, call)
	a.trustMu.Lock()
	defer a.trustMu.Unlock()
	_, ok := a.trusted[key]
	return ok
}

func approvalKey(def tools.Definition, call tools.Call) string {
	var args map[string]any
	if len(call.Arguments) > 0 {
		_ = json.Unmarshal(call.Arguments, &args)
	}
	target := ""
	for _, field := range []string{"path", "executable", "command"} {
		value, ok := args[field].(string)
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if field == "path" {
			value = normalizeTrustPath(value)
		}
		target = value
		break
	}
	if target == "" {
		sum := sha256.Sum256(call.Arguments)
		target = hex.EncodeToString(sum[:8])
	}
	return def.Name + "\x00" + target
}

// normalizeTrustPath makes equivalent workspace-relative paths share one trust
// entry. a.txt and ./a.txt are the same file.
func normalizeTrustPath(value string) string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	value = path.Clean(value)
	value = strings.TrimPrefix(value, "./")
	if value == "" || value == "." {
		return "."
	}
	return value
}

func describeCall(def tools.Definition, call tools.Call) string {
	var args map[string]any
	if len(call.Arguments) > 0 {
		_ = json.Unmarshal(call.Arguments, &args)
	}
	switch def.Name {
	case "write_file":
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		return fmt.Sprintf("write %s (%d bytes)", path, len(content))
	case "apply_patch":
		patch, _ := args["patch"].(string)
		return fmt.Sprintf("apply patch (%d bytes, %d lines)", len(patch), strings.Count(patch, "\n"))
	case "run_command":
		exe, _ := args["executable"].(string)
		return fmt.Sprintf("run %s", truncateRunes(strings.TrimSpace(exe+" "+describeArgv(args["args"])), 200))
	default:
		return fmt.Sprintf("%s (%d argument bytes)", def.Name, len(call.Arguments))
	}
}

func describeArgv(value any) string {
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

func isDestructiveCall(def tools.Definition, call tools.Call) bool {
	if def.Category != tools.CategoryExec {
		return false
	}
	var args struct {
		Executable string   `json:"executable"`
		Args       []string `json:"args"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return false
	}
	return security.IsDestructive(args.Executable, args.Args)
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "..."
}
