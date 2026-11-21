package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"velo/internal/config"
	"velo/internal/tools"
)

func (a *App) versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:         "version",
		Short:       "Print the velo version",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{skipConfigAnnotation: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := io.WriteString(a.stdout, a.versionText())
			return err
		},
	}
}

func (a *App) initCommand() *cobra.Command {
	return &cobra.Command{
		Use:         "init",
		Short:       "Create a starter configuration",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{skipConfigAnnotation: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			flagWorkspace, _ := cmd.Flags().GetString("workspace")
			workspace, err := a.resolveWorkspace(flagWorkspace)
			if err != nil {
				return err
			}
			path, created, err := config.WriteStarter(workspace)
			if err != nil {
				return err
			}
			if !filepath.IsAbs(path) {
				if abs, absErr := filepath.Abs(path); absErr == nil {
					path = abs
				}
			}
			if created {
				_, err = fmt.Fprintf(a.stdout, "created %s\n", path)
			} else {
				_, err = fmt.Fprintf(a.stdout, "config already exists: %s\n", path)
			}
			return err
		},
	}
}

func (a *App) configCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Print the effective configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := a.cfg.MarshalRedacted()
			if err != nil {
				return err
			}
			if _, err := a.stdout.Write(data); err != nil {
				return err
			}
			if len(data) == 0 || data[len(data)-1] != '\n' {
				_, err = fmt.Fprintln(a.stdout)
			}
			return err
		},
	}
}

func (a *App) providersCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "providers",
		Short: "List registered providers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			names := append([]string(nil), a.registry.Names()...)
			sort.Strings(names)

			// Min-width keeps short values from collapsing into the next column
			// when the output is a pipe rather than a terminal.
			w := tabwriter.NewWriter(a.stdout, 10, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "NAME\tSTREAM\tTOOLS\tUSAGE\tCONFIGURED"); err != nil {
				return err
			}
			opts := a.providerOptions()
			for _, name := range names {
				p, err := a.registry.New(name, opts)
				if err != nil {
					if _, werr := fmt.Fprintf(a.stderr, "velo: provider: %s: %v\n", name, err); werr != nil {
						return werr
					}
					continue
				}
				caps := p.Capabilities()
				configured := "no"
				if name == a.cfg.Provider.Name {
					configured = "yes"
				}
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, yesNo(caps.Streaming), yesNo(caps.Tools), yesNo(caps.Usage), configured); err != nil {
					_ = p.Close()
					return err
				}
				if err := p.Close(); err != nil {
					return err
				}
			}
			return w.Flush()
		},
	}
}

func (a *App) modelsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "Show the configured model",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.writeModels(a.stdout)
		},
	}
}

func (a *App) toolsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "tools",
		Short: "List configured tools",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.writeTools(a.stdout)
		},
	}
}

func (a *App) statusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show runtime status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.writeStatus(a.stdout)
		},
	}
}

func (a *App) doctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run environment checks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ok, warn, failed := 0, 0, 0
			report := func(status, format string, values ...any) {
				switch status {
				case "ok":
					ok++
				case "warn":
					warn++
				default:
					failed++
				}
				_, _ = fmt.Fprintf(a.stdout, "[%s] %s\n", status, fmt.Sprintf(format, values...))
			}

			if info, err := os.Stat(a.workspace); err != nil {
				report("fail", "workspace %s: %v", a.workspace, err)
			} else if !info.IsDir() {
				report("fail", "workspace %s is not a directory", a.workspace)
			} else {
				report("ok", "workspace %s is a directory", a.workspace)
			}

			switch {
			case a.loaded:
				report("ok", "config loaded from %s", a.configPath)
			case a.explicit:
				report("fail", "config %s could not be loaded", a.configPath)
			default:
				report("warn", "config not found at %s; run `velo init`", a.configPath)
			}

			names := append([]string(nil), a.registry.Names()...)
			sort.Strings(names)
			available := strings.Join(names, ", ")
			if available == "" {
				available = "(none)"
			}
			switch {
			case a.cfg.Provider.Name == "":
				report("warn", "provider is not configured; available: %s", available)
			case a.registry.Has(a.cfg.Provider.Name):
				report("ok", "provider %s registered", a.cfg.Provider.Name)
			default:
				report("warn", "provider %s is not registered; available: %s", a.cfg.Provider.Name, available)
			}

			if !providerRequiresAPIKey(a.cfg.Provider.Name) {
				report("ok", "api key not required for provider %s", a.cfg.Provider.Name)
			} else if a.cfg.Provider.APIKeyEnv == "" {
				report("ok", "api key environment variable not required")
			} else if value, set := a.lookupEnv(a.cfg.Provider.APIKeyEnv); set && value != "" {
				report("ok", "api key environment variable %s is set", a.cfg.Provider.APIKeyEnv)
			} else {
				report("warn", "api key environment variable %s is not set", a.cfg.Provider.APIKeyEnv)
			}

			sessionsDir := a.sessionsDir()
			info, statErr := os.Stat(sessionsDir)
			switch {
			case statErr == nil && info.IsDir():
				report("ok", "sessions directory %s is usable", sessionsDir)
				if _, err := a.sessionStore(); err != nil {
					report("fail", "session store: %v", err)
				} else {
					report("ok", "session store is usable")
				}
			case statErr == nil:
				report("fail", "sessions directory %s is not a directory", sessionsDir)
			case os.IsNotExist(statErr):
				report("warn", "sessions directory %s does not exist yet", sessionsDir)
			default:
				report("fail", "sessions directory %s: %v", sessionsDir, statErr)
			}

			if _, _, all, err := a.buildAllTools(); err != nil {
				report("fail", "tools: %v", err)
			} else if len(a.cfg.Tools.Enabled) == 0 {
				report("warn", "no tools enabled; configure tools.enabled")
			} else {
				var unknown []string
				for _, name := range a.cfg.Tools.Enabled {
					if _, ok := all.Get(name); !ok {
						unknown = append(unknown, name)
					}
				}
				if len(unknown) > 0 {
					report("warn", "unknown tools in tools.enabled: %s", strings.Join(unknown, ", "))
				} else {
					report("ok", "%d configured tools available: %s", len(a.cfg.Tools.Enabled), strings.Join(a.cfg.Tools.Enabled, ", "))
				}
			}

			_, _ = fmt.Fprintf(a.stdout, "doctor: %d ok, %d warn, %d failed\n", ok, warn, failed)
			if failed > 0 {
				return fmt.Errorf("doctor: %d check(s) failed", failed)
			}
			return nil
		},
	}
}

func (a *App) writeModels(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "provider: %s\nmodel: %s\n", a.cfg.Provider.Name, a.cfg.Provider.Model); err != nil {
		return err
	}
	p, err := a.registry.New(a.cfg.Provider.Name, a.providerOptions())
	if err != nil {
		_, werr := fmt.Fprintf(w, "available: no (%v)\n", err)
		return werr
	}
	defer p.Close()
	caps := p.Capabilities()
	_, err = fmt.Fprintf(w, "streaming: %s\ntools: %s\nusage: %s\n", yesNo(caps.Streaming), yesNo(caps.Tools), yesNo(caps.Usage))
	return err
}

func (a *App) writeTools(w io.Writer) error {
	// `velo tools` is intentionally lenient: it must show missing configured
	// tools instead of failing before the listing.
	_, _, registry, err := a.buildAllTools()
	if err != nil {
		return err
	}
	available := make(map[string]tools.Definition, registry.Len())
	for _, def := range registry.List() {
		available[def.Name] = def
	}
	if len(a.cfg.Tools.Enabled) == 0 {
		_, err := fmt.Fprintln(w, "no tools enabled")
		return err
	}
	if _, err := fmt.Fprintln(w, "configured tools:"); err != nil {
		return err
	}
	for _, name := range a.cfg.Tools.Enabled {
		def, ok := available[name]
		if !ok {
			if _, err := fmt.Fprintf(w, "  %s (not available)\n", name); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "  %s (%s)\n", name, def.Category); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w, "available tools:"); err != nil {
		return err
	}
	if len(available) == 0 {
		_, err := fmt.Fprintln(w, "  (none)")
		return err
	}
	names := make([]string, 0, len(available))
	for name := range available {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		def := available[name]
		label := string(def.Category)
		if def.ReadOnly {
			label += ", read-only"
		}
		if _, err := fmt.Fprintf(w, "  %s (%s)\n", name, label); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) versionText() string {
	return fmt.Sprintf("velo %s (%s/%s)\ncommit: %s\nbuilt: %s\n", a.version, runtime.GOOS, runtime.GOARCH, a.commit, a.built)
}

func (a *App) writeStatus(w io.Writer) error {
	configPath := "(defaults)"
	configState := "defaults"
	if a.loaded {
		configPath = a.configPath
		configState = "loaded"
	}
	security := a.cfg.Security
	sessions := countSessionFiles(a.sessionsDir())
	_, err := fmt.Fprintf(w,
		"version: %s\n"+
			"commit: %s\n"+
			"built: %s\n"+
			"workspace: %s\n"+
			"config: %s\n"+
			"config state: %s\n"+
			"provider: %s\n"+
			"model: %s\n"+
			"sessions: %s (%d sessions)\n"+
			"security: mode=%s execute=%s shell=%s network=%s file writes=%s\n"+
			"context budget: in=%d out=%d\n"+
			"agent: max turns=%d, max tool calls/turn=%d, max output tokens=%d\n"+
			"timeouts: request=%s tool=%s total=%s\n"+
			"ui: enabled=%s theme=%s\n"+
			"go: %s %s/%s\n",
		a.version,
		a.commit,
		a.built,
		a.workspace,
		configPath,
		configState,
		a.cfg.Provider.Name,
		a.cfg.Provider.Model,
		a.cfg.Sessions.Directory,
		sessions,
		security.DefaultMode,
		allowed(security.AllowExecute),
		allowed(security.AllowShell),
		allowed(security.AllowNetwork),
		allowed(security.AllowFileWrite),
		a.cfg.Context.MaxInputTokens,
		a.cfg.Context.ReserveOutputTokens,
		a.cfg.Agent.MaxTurns,
		a.cfg.Agent.MaxToolCallsPerTurn,
		a.cfg.Agent.MaxOutputTokens,
		a.cfg.Agent.RequestTimeout.String(),
		a.cfg.Agent.ToolTimeout.String(),
		a.cfg.Agent.TotalTimeout.String(),
		yesNo(a.cfg.UI.Enabled),
		a.cfg.UI.Theme,
		runtime.Version(),
		runtime.GOOS,
		runtime.GOARCH,
	)
	return err
}

func (a *App) sessionsDir() string {
	dir := a.cfg.Sessions.Directory
	if dir == "" {
		return filepath.Join(a.workspace, ".velo", "sessions")
	}
	if !filepath.IsAbs(dir) {
		return filepath.Join(a.workspace, dir)
	}
	return dir
}

func providerRequiresAPIKey(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "mock":
		return false
	default:
		return true
	}
}

func (a *App) report(name string) (string, error) {
	var buf strings.Builder
	var err error
	switch name {
	case "status":
		err = a.writeStatus(&buf)
	case "config":
		var data []byte
		data, err = a.cfg.MarshalRedacted()
		if err == nil {
			buf.Write(data)
			if len(data) == 0 || data[len(data)-1] != '\n' {
				buf.WriteByte('\n')
			}
		}
	case "models":
		err = a.writeModels(&buf)
	case "tools":
		err = a.writeTools(&buf)
	default:
		err = fmt.Errorf("unknown report %q", name)
	}
	return buf.String(), err
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func allowed(value bool) string {
	if value {
		return "allowed"
	}
	return "denied"
}

func countSessionFiles(dir string) int {
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			count++
		}
	}
	return count
}
