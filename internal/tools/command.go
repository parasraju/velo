package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"velo/internal/security"
)

var schemaRunCommand = json.RawMessage(`{
	"type": "object",
	"properties": {
		"executable": {"type": "string", "description": "Program to execute, without shell interpolation."},
		"args": {"type": "array", "items": {"type": "string"}, "description": "Argument vector passed literally to the executable."},
		"dir": {"type": "string", "description": "Workspace-relative working directory. Defaults to the workspace root."}
	},
	"required": ["executable"],
	"additionalProperties": false
}`)

const defaultCommandOutputBytes = 32 << 10

// commandWaitDelay is how long run_command waits for stdout and stderr to
// close after the child exits or the context is cancelled. Tests may shorten
// it. Zero would wait forever, which is the bug this delay exists to close.
var commandWaitDelay = 2 * time.Second

type runCommandArgs struct {
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
	Dir        string   `json:"dir"`
}

type runCommandTool struct {
	policy      *security.Policy
	outputBytes int
}

func (t *runCommandTool) Definition() Definition {
	return Definition{
		Name:        "run_command",
		Description: "Run a program with an explicit argument vector and working directory. Requires approval; shell interpretation is never used.",
		InputSchema: schemaRunCommand,
		Category:    CategoryExec,
	}
}

func (t *runCommandTool) Execute(ctx context.Context, call Call) (Result, error) {
	var args runCommandArgs
	if err := decodeArgs(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	args.Executable = strings.TrimSpace(args.Executable)
	if args.Executable == "" {
		return Result{}, fmt.Errorf("%w: executable is required", ErrInvalidArguments)
	}
	for i, arg := range args.Args {
		if strings.IndexByte(arg, 0) >= 0 {
			return Result{}, fmt.Errorf("%w: argument %d contains NUL byte", ErrInvalidArguments, i)
		}
	}
	dir, err := t.policy.CheckCommand(security.CommandSpec{
		Executable: args.Executable,
		Args:       args.Args,
		Dir:        args.Dir,
	})
	if err != nil {
		return Result{}, denyError(err)
	}

	cmd := exec.CommandContext(ctx, args.Executable, args.Args...)
	cmd.Dir = dir
	cmd.Env = commandEnv(nil)
	// WaitDelay unblocks Wait when a grandchild inherited stdout or stderr and
	// keeps the pipe open after the direct child exits or is cancelled.
	cmd.WaitDelay = commandWaitDelay
	setupProcess(cmd)
	maxBytes := t.outputBytes
	if maxBytes <= 0 {
		maxBytes = defaultCommandOutputBytes
	}
	stdout := newLimitWriter(maxBytes)
	stderr := newLimitWriter(maxBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	started := time.Now()
	runErr := cmd.Run()
	duration := time.Since(started)

	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return Result{}, fmt.Errorf("%w: command exceeded its timeout", ErrTimeout)
		}
		return Result{}, ctxErr
	}

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return Result{}, fmt.Errorf("run_command: %w", runErr)
		}
		exitCode = exitErr.ExitCode()
	}

	out := stdout.String()
	errOut := stderr.String()
	var b strings.Builder
	fmt.Fprintf(&b, "exit code: %d\n", exitCode)
	b.WriteString("stdout:\n")
	if out == "" {
		b.WriteString("(empty)\n")
	} else {
		b.WriteString(out)
		if !strings.HasSuffix(out, "\n") {
			b.WriteString("\n")
		}
	}
	if stdout.Truncated() {
		fmt.Fprintf(&b, "[stdout truncated at %d bytes]\n", maxBytes)
	}
	b.WriteString("stderr:\n")
	if errOut == "" {
		b.WriteString("(empty)\n")
	} else {
		b.WriteString(errOut)
		if !strings.HasSuffix(errOut, "\n") {
			b.WriteString("\n")
		}
	}
	if stderr.Truncated() {
		fmt.Fprintf(&b, "[stderr truncated at %d bytes]\n", maxBytes)
	}

	return Result{
		CallID:  call.ID,
		Content: b.String(),
		IsError: exitCode != 0,
		Metadata: map[string]any{
			"executable":   args.Executable,
			"args_count":   len(args.Args),
			"dir":          dir,
			"exit_code":    exitCode,
			"duration_ms":  duration.Milliseconds(),
			"stdout_bytes": stdout.Len(),
			"stderr_bytes": stderr.Len(),
			"stdout_cut":   stdout.Truncated(),
			"stderr_cut":   stderr.Truncated(),
			"destructive":  security.IsDestructive(args.Executable, args.Args),
			"network":      security.IsNetworkCommand(args.Executable, args.Args),
		},
	}, nil
}

type limitWriter struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func newLimitWriter(max int) *limitWriter {
	if max < 0 {
		max = 0
	}
	return &limitWriter{max: max}
}

func (w *limitWriter) Write(p []byte) (int, error) {
	original := len(p)
	remaining := w.max - w.buf.Len()
	if remaining <= 0 {
		w.truncated = w.truncated || len(p) > 0
		return original, nil
	}
	if len(p) > remaining {
		w.truncated = true
		p = p[:remaining]
	}
	_, _ = w.buf.Write(p)
	return original, nil
}

func (w *limitWriter) String() string  { return w.buf.String() }
func (w *limitWriter) Len() int        { return w.buf.Len() }
func (w *limitWriter) Truncated() bool { return w.truncated }
