package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"velo/internal/agent"
	"velo/internal/contextmgr"
	"velo/internal/session"
)

type trackingWriter struct {
	w     io.Writer
	wrote bool
	last  byte
}

func (t *trackingWriter) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	if n > 0 {
		t.wrote = true
		t.last = p[n-1]
	}
	return n, err
}

func (a *App) runOneShot(ctx context.Context, objective string) error {
	rt, err := a.newRuntime()
	if err != nil {
		return err
	}
	defer rt.Close()

	sess, err := rt.createSession()
	if err != nil {
		return err
	}
	return a.runWithRuntime(ctx, rt, sess, objective)
}

func (a *App) runREPL(ctx context.Context) error {
	if a.useTUI() {
		return a.runTUI(ctx)
	}

	rt, err := a.newRuntime()
	if err != nil {
		return err
	}
	defer rt.Close()

	sess, err := rt.createSession()
	if err != nil {
		return err
	}

	fmt.Fprintf(a.stdout, "velo %s | workspace %s | session %s\n", a.version, a.workspace, sess.ID())
	fmt.Fprintln(a.stdout, "Type a task, :sessions to list sessions, :quit to exit.")

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprint(a.stdout, "velo> ")
		line, readErr := a.reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		line = strings.TrimSpace(line)
		if line == "" {
			if readErr == io.EOF {
				fmt.Fprintln(a.stdout)
				return nil
			}
			continue
		}
		switch line {
		case ":quit", ":exit", "quit", "exit":
			return nil
		case ":sessions":
			if err := a.printSessions(rt.store); err != nil {
				fmt.Fprintf(a.stderr, "velo: %v\n", err)
			}
			if readErr == io.EOF {
				return nil
			}
			continue
		}
		if err := a.runWithRuntime(ctx, rt, sess, line); err != nil {
			fmt.Fprintf(a.stderr, "velo: %v\n", err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		if readErr == io.EOF {
			return nil
		}
	}
}

func (a *App) runWithRuntime(ctx context.Context, rt *runtimeEnv, sess *session.Session, objective string) error {
	stream := &trackingWriter{w: a.stdout}
	var output io.Writer
	if a.cfg.Agent.Stream {
		output = stream
	}

	runner, err := agent.New(agent.Options{
		Provider:            rt.prov,
		Tools:               rt.dispatcher,
		Session:             sess,
		Workspace:           a.workspace,
		MaxTurns:            a.cfg.Agent.MaxTurns,
		MaxToolCallsPerTurn: a.cfg.Agent.MaxToolCallsPerTurn,
		MaxOutputTokens:     a.cfg.Agent.MaxOutputTokens,
		RequestTimeout:      a.cfg.Agent.RequestTimeout.Std(),
		TotalTimeout:        a.cfg.Agent.TotalTimeout.Std(),
		Stream:              a.cfg.Agent.Stream,
		Output:              output,
		Sink:                a.sink,
		SystemPrompt:        a.systemPrompt(rt.registry),
		ContextBudget: contextmgr.Budget{
			MaxInputTokens:      a.cfg.Context.MaxInputTokens,
			ReserveOutputTokens: a.cfg.Context.ReserveOutputTokens,
			SafetyMargin:        a.cfg.Context.SafetyMargin,
		},
		CompactThreshold: a.cfg.Context.CompactThreshold,
		CompactTarget:    a.cfg.Context.CompactTarget,
	})
	if err != nil {
		return err
	}

	result, runErr := runner.Run(ctx, objective)
	if stream.wrote && stream.last != '\n' {
		fmt.Fprintln(a.stdout)
	}
	if !stream.wrote && result.Content != "" {
		fmt.Fprintln(a.stdout, result.Content)
	}
	a.printResult(result)
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) {
			return runErr
		}
		return fmt.Errorf("%s: %w", result.Status, runErr)
	}
	if result.Status != agent.StateCompleted {
		return fmt.Errorf("agent ended in state %s", result.Status)
	}
	return nil
}

func (a *App) printResult(result agent.Result) {
	fmt.Fprintf(a.stdout, "session: %s\n", result.SessionID)
	fmt.Fprintf(a.stdout, "status: %s\n", result.Status)
	fmt.Fprintf(a.stdout, "turns: %d | tool calls: %d\n", result.Turns, result.ToolCalls)
	if result.Usage != nil {
		fmt.Fprintf(a.stdout, "usage: input=%d output=%d total=%d\n",
			result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.TotalTokens)
	}
	if result.Verification != "" {
		detail := result.VerificationDetail
		if detail == "" {
			detail = "no verification checks were executed"
		}
		fmt.Fprintf(a.stdout, "verification: %s (%s)\n", result.Verification, detail)
	}
}
