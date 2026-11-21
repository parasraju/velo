package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"velo/internal/config"
	"velo/internal/events"
	"velo/internal/provider"
	"velo/internal/provider/gemini"
	"velo/internal/provider/mock"
	"velo/internal/provider/openai"
	"velo/internal/tools"
)

const skipConfigAnnotation = "velo.skipConfig"

type Options struct {
	Version    string
	Commit     string
	Date       string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Workspace  string
	ConfigPath string
	Registry   *provider.Registry
	LookupEnv  func(string) (string, bool)
	Context    context.Context
	// Approver overrides the interactive approval prompt. Tests and embedding
	// applications use it to supply a deterministic policy decision.
	Approver tools.Approver
	// Sink receives live run events for user interfaces. It is optional; when
	// set, the agent and approval layers publish their lifecycle here in
	// addition to any plain-text output.
	Sink events.Emitter
}

type App struct {
	opts       Options
	version    string
	commit     string
	built      string
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	lookupEnv  func(string) (string, bool)
	registry   *provider.Registry
	workspace  string
	configPath string
	cfg        config.Config
	loaded     bool
	explicit   bool
	reader     *bufio.Reader
	sink       events.Emitter
	plain      bool
	trustMu    sync.Mutex
	trusted    map[string]struct{}
}

func New(opts Options) (*App, error) {
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.Commit == "" {
		opts.Commit = "unknown"
	}
	if opts.Date == "" {
		opts.Date = "unknown"
	}
	if opts.LookupEnv == nil {
		opts.LookupEnv = os.LookupEnv
	}

	registry := opts.Registry
	if registry == nil {
		registry = provider.NewRegistry()
	}

	a := &App{
		opts:      opts,
		version:   opts.Version,
		commit:    opts.Commit,
		built:     opts.Date,
		stdin:     opts.Stdin,
		stdout:    opts.Stdout,
		stderr:    opts.Stderr,
		lookupEnv: opts.LookupEnv,
		registry:  registry,
		cfg:       config.Default(),
		reader:    bufio.NewReaderSize(opts.Stdin, 64*1024),
		sink:      opts.Sink,
		trusted:   make(map[string]struct{}),
	}

	if opts.Registry == nil {
		if err := a.registry.Register("mock", func(o provider.Options) (provider.Provider, error) {
			return mock.NewDemo(o.Model), nil
		}); err != nil {
			return nil, err
		}
		for _, name := range []string{"openai", "openai-compatible"} {
			if err := a.registry.Register(name, func(o provider.Options) (provider.Provider, error) {
				return openai.New(o)
			}); err != nil {
				return nil, err
			}
		}
		if err := a.registry.Register("gemini", func(o provider.Options) (provider.Provider, error) {
			return gemini.New(o)
		}); err != nil {
			return nil, err
		}
	}

	return a, nil
}

func (a *App) Execute(args []string) error {
	root := a.newRootCommand()
	root.SetArgs(args)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	if a.opts.Context != nil {
		root.SetContext(a.opts.Context)
	}
	return root.Execute()
}

func (a *App) newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "velo",
		Short:         "VELO agent runtime",
		Args:          cobra.ArbitraryArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       a.version,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return a.runREPL(cmd.Context())
			}
			return a.runOneShot(cmd.Context(), strings.Join(args, " "))
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if plain, err := cmd.Flags().GetBool("plain"); err == nil {
				a.plain = plain
			}
			// `velo --version` must not require a workspace or config.
			if cmd.Parent() == nil {
				if f := cmd.Flags().Lookup("version"); f != nil && f.Changed {
					return nil
				}
			}
			if cmd.Annotations[skipConfigAnnotation] == "true" || cmd.Parent() != nil && cmd.Parent().Annotations[skipConfigAnnotation] == "true" {
				return nil
			}
			return a.loadConfig(cmd)
		},
	}
	root.SetVersionTemplate(a.versionText())

	root.PersistentFlags().String("config", "", "path to the configuration file")
	root.PersistentFlags().String("workspace", "", "workspace directory")
	root.PersistentFlags().Bool("plain", false, "use the line REPL instead of the full-screen terminal UI")

	root.AddCommand(
		a.versionCommand(),
		a.initCommand(),
		a.configCommand(),
		a.providersCommand(),
		a.modelsCommand(),
		a.toolsCommand(),
		a.statusCommand(),
		a.doctorCommand(),
		a.sessionsCommand(),
		a.resumeCommand(),
	)

	return root
}

func (a *App) loadConfig(cmd *cobra.Command) error {
	flagWorkspace, _ := cmd.Flags().GetString("workspace")
	flagConfig, _ := cmd.Flags().GetString("config")

	locateWorkspace, err := a.resolveWorkspace(flagWorkspace)
	if err != nil {
		return err
	}
	envWorkspace, envWorkspaceSet := a.lookupEnv("VELO_WORKSPACE")
	envWorkspaceSet = envWorkspaceSet && envWorkspace != ""

	envConfig, envConfigSet := a.lookupEnv("VELO_CONFIG")
	envConfigSet = envConfigSet && envConfig != ""

	var (
		path     string
		explicit bool
	)
	switch {
	case flagConfig != "":
		path, explicit = flagConfig, true
	case envConfigSet:
		path, explicit = envConfig, true
	case a.opts.ConfigPath != "":
		path, explicit = a.opts.ConfigPath, true
	default:
		path = filepath.Join(locateWorkspace, ".velo", "config.yaml")
	}

	if explicit && !filepath.IsAbs(path) {
		abs, absErr := filepath.Abs(path)
		if absErr != nil {
			return absErr
		}
		path = abs
	}

	cfg, found, err := config.LoadOptional(path, a.lookupEnv)
	if err != nil {
		return fmt.Errorf("load config %q: %w", path, err)
	}
	if explicit && !found {
		return fmt.Errorf("config file not found: %s", path)
	}

	workspace := locateWorkspace
	if flagWorkspace == "" && !envWorkspaceSet && a.opts.Workspace == "" && cfg.App.Workspace != "" {
		abs, absErr := filepath.Abs(cfg.App.Workspace)
		if absErr != nil {
			return fmt.Errorf("resolve workspace %q: %w", cfg.App.Workspace, absErr)
		}
		workspace = filepath.Clean(abs)
	}
	cfg.App.Workspace = workspace

	if cfg.Sessions.Directory != "" && !filepath.IsAbs(cfg.Sessions.Directory) {
		cfg.Sessions.Directory = filepath.Clean(filepath.Join(workspace, cfg.Sessions.Directory))
	}

	a.workspace = workspace
	a.configPath = path
	a.cfg = cfg
	a.loaded = found
	a.explicit = explicit

	return nil
}

func (a *App) resolveWorkspace(flagWorkspace string) (string, error) {
	workspace := ""
	switch {
	case flagWorkspace != "":
		workspace = flagWorkspace
	case envSet(a.lookupEnv, "VELO_WORKSPACE"):
		workspace, _ = a.lookupEnv("VELO_WORKSPACE")
	case a.opts.Workspace != "":
		workspace = a.opts.Workspace
	default:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		workspace = cwd
	}
	if workspace == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		workspace = cwd
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func (a *App) providerOptions() provider.Options {
	opts := provider.Options{
		Model:      a.cfg.Provider.Model,
		BaseURL:    a.cfg.Provider.BaseURL,
		Headers:    a.cfg.Provider.Headers,
		MaxRetries: a.cfg.Provider.MaxRetries,
	}
	if a.cfg.Provider.APIKeyEnv != "" {
		opts.APIKey, _ = a.lookupEnv(a.cfg.Provider.APIKeyEnv)
	}
	return opts
}

// emit publishes an event to the app's sink, stamping time when unset. A nil
// sink makes this a no-op, which is the plain-REPL and one-shot path.
func (a *App) emit(e events.Event) {
	if a.sink == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	a.sink.Emit(e)
}

func envSet(lookup func(string) (string, bool), key string) bool {
	if lookup == nil {
		return false
	}
	v, ok := lookup(key)
	return ok && v != ""
}
