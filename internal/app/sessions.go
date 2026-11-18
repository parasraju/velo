package app

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"velo/internal/protocol"
	"velo/internal/session"
)

func (a *App) sessionsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List or inspect saved sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := a.sessionStore()
			if err != nil {
				return err
			}
			return a.printSessions(store)
		},
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List saved sessions",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				store, err := a.sessionStore()
				if err != nil {
					return err
				}
				return a.printSessions(store)
			},
		},
		&cobra.Command{
			Use:   "inspect <id>",
			Short: "Inspect a saved session",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				store, err := a.sessionStore()
				if err != nil {
					return err
				}
				return a.printSession(store, args[0])
			},
		},
	)
	return cmd
}

func (a *App) resumeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <id> [task]",
		Short: "Resume a saved session",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			objective := strings.TrimSpace(strings.Join(args[1:], " "))
			if objective == "" {
				objective = "Continue the previous task."
			}
			rt, err := a.newRuntime()
			if err != nil {
				return err
			}
			defer rt.Close()
			sess, err := rt.openSession(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "resuming session %s\n", sess.ID())
			return a.runWithRuntime(cmd.Context(), rt, sess, objective)
		},
	}
}

func (a *App) printSessions(store *session.Store) error {
	list, err := store.List()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		_, err := fmt.Fprintln(a.stdout, "no sessions")
		return err
	}
	w := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tSTATUS\tTURNS\tMESSAGES\tUPDATED\tWORKSPACE"); err != nil {
		return err
	}
	for _, summary := range list {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%s\n",
			summary.ID,
			summary.Status,
			summary.Turns,
			summary.Messages,
			summary.UpdatedAt.Format("2006-01-02 15:04:05"),
			summary.Workspace,
		); err != nil {
			return err
		}
	}
	return w.Flush()
}

func (a *App) printSession(store *session.Store, id string) error {
	sess, err := store.Open(id)
	if err != nil {
		return err
	}
	path, err := store.Path(id)
	if err != nil {
		return err
	}
	state := sess.State()
	fmt.Fprintf(a.stdout, "id: %s\n", sess.ID())
	fmt.Fprintf(a.stdout, "workspace: %s\n", sess.Workspace())
	fmt.Fprintf(a.stdout, "status: %s\n", state.Status)
	fmt.Fprintf(a.stdout, "objective: %s\n", oneLine(state.Objective, 200))
	fmt.Fprintf(a.stdout, "turns: %d\n", state.Turns)
	fmt.Fprintf(a.stdout, "created: %s\n", sess.CreatedAt().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(a.stdout, "updated: %s\n", sess.UpdatedAt().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(a.stdout, "file: %s\n", path)

	events := sess.Events()
	counts := make(map[string]int, len(events))
	for _, event := range events {
		counts[event.Kind]++
	}
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	fmt.Fprintln(a.stdout, "events:")
	if len(kinds) == 0 {
		fmt.Fprintln(a.stdout, "  (none)")
	}
	for _, kind := range kinds {
		fmt.Fprintf(a.stdout, "  %s: %d\n", kind, counts[kind])
	}

	messages := sess.Messages()
	fmt.Fprintf(a.stdout, "transcript: %d message(s)\n", len(messages))
	for i, message := range messages {
		fmt.Fprintf(a.stdout, "  %d. %s: %s\n", i+1, message.Role, describeMessage(message))
	}
	return nil
}

func describeMessage(message protocol.Message) string {
	text := oneLine(message.Text(), 200)
	calls := message.ToolCalls()
	if len(calls) == 0 {
		return text
	}
	names := make([]string, len(calls))
	for i, call := range calls {
		names[i] = call.Name
	}
	description := "tool calls: " + strings.Join(names, ", ")
	if text != "" {
		return text + " | " + description
	}
	return description
}

func oneLine(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	if max <= 0 || len(text) <= max {
		return text
	}
	cut := text[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "..."
}
