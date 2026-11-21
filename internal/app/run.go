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

	rt, err := a.newInteractiveRuntime()
	if err != nil {
		return err
	}
	defer rt.Close()

	sess, err := rt.createSession()
	if err != nil {
		return err
	}

	fmt.Fprintf(a.stdout, "velo %s | workspace %s | session %s\n", a.version, a.workspace, sess.ID())
	if rt.provider() == nil {
		fmt.Fprintln(a.stdout, "No provider connected. /providers lists them, /provider <name> selects one, /connect sets an API key.")
	}
	fmt.Fprintln(a.stdout, "Type a task, /sessions to list sessions, /quit to exit.")

	awaitKey := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if awaitKey {
			fmt.Fprint(a.stdout, "api key: ")
		} else {
			fmt.Fprint(a.stdout, "velo> ")
		}
		line, readErr := a.reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		line = strings.TrimSpace(line)
		if awaitKey {
			awaitKey = false
			if line != "" && !strings.HasPrefix(line, ":") {
				msg, err := rt.connectKey(line)
				if err != nil {
					fmt.Fprintf(a.stderr, "velo: %v\n", err)
				} else {
					fmt.Fprintln(a.stdout, msg)
				}
			} else {
				fmt.Fprintln(a.stderr, "velo: connect cancelled")
			}
			if readErr == io.EOF {
				return nil
			}
			continue
		}
		if line == "" {
			if readErr == io.EOF {
				fmt.Fprintln(a.stdout)
				return nil
			}
			continue
		}
		switch {
		case line == "/quit" || line == "/exit" || line == "quit" || line == "exit":
			return nil
		case line == "/sessions":
			if err := a.printSessions(rt.store); err != nil {
				fmt.Fprintf(a.stderr, "velo: %v\n", err)
			}
		case line == "/providers" || line == "/provider":
			fmt.Fprint(a.stdout, rt.formatProviders())
		case line == "/models":
			fmt.Fprint(a.stdout, formatModels(rt))
		case strings.HasPrefix(line, "/provider "):
			fields := strings.Fields(line)
			model := ""
			if len(fields) >= 3 {
				model = fields[2]
			}
			msg, err := rt.useProvider(fields[1], model)
			if err != nil {
				fmt.Fprintf(a.stderr, "velo: %v\n", err)
			} else {
				fmt.Fprintln(a.stdout, msg)
				if providerRequiresAPIKey(fields[1]) && rt.provider() == nil {
					fmt.Fprintf(a.stdout, "Enter the API key for %s. It stays in this process and is not saved.\n", fields[1])
					awaitKey = true
				}
			}
		case strings.HasPrefix(line, "/model "):
			model := strings.TrimSpace(strings.TrimPrefix(line, "/model "))
			msg, err := rt.useModel(model)
			if err != nil {
				fmt.Fprintf(a.stderr, "velo: %v\n", err)
			} else {
				fmt.Fprintln(a.stdout, msg)
			}
		case line == "/connect":
			if !providerRequiresAPIKey(a.cfg.Provider.Name) {
				fmt.Fprintf(a.stdout, "%s does not use an API key\n", a.cfg.Provider.Name)
				break
			}
			fmt.Fprintf(a.stdout, "Enter the API key for %s. It stays in this process and is not saved.\n", a.cfg.Provider.Name)
			awaitKey = true
		default:
			if rt.provider() == nil {
				fmt.Fprintln(a.stderr, "velo: no provider connected. /providers to choose one, /connect to set an API key.")
			} else if err := a.runWithRuntime(ctx, rt, sess, line); err != nil {
				fmt.Fprintf(a.stderr, "velo: %v\n", err)
				if ctx.Err() != nil {
					return ctx.Err()
				}
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

	prov := rt.provider()
	if prov == nil {
		return fmt.Errorf("no provider connected")
	}
	runner, err := agent.New(agent.Options{
		Provider:            prov,
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
	if result.Truncated {
		fmt.Fprintln(a.stdout, "truncated: output hit the length limit")
	}
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
