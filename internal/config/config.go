package config

import "time"

type Config struct {
	App      AppConfig      `yaml:"app"`
	Agent    AgentConfig    `yaml:"agent"`
	Provider ProviderConfig `yaml:"provider"`
	Context  ContextConfig  `yaml:"context"`
	Security SecurityConfig `yaml:"security"`
	Sessions SessionsConfig `yaml:"sessions"`
	Tools    ToolsConfig    `yaml:"tools"`
	MCP      MCPConfig      `yaml:"mcp"`
	UI       UIConfig       `yaml:"ui"`
}

type AppConfig struct {
	Workspace string `yaml:"workspace"`
	LogLevel  string `yaml:"log_level"`
}

type AgentConfig struct {
	MaxTurns            int      `yaml:"max_turns"`
	MaxToolCallsPerTurn int      `yaml:"max_tool_calls_per_turn"`
	RequestTimeout      Duration `yaml:"request_timeout"`
	ToolTimeout         Duration `yaml:"tool_timeout"`
	TotalTimeout        Duration `yaml:"total_timeout"`
	Stream              bool     `yaml:"stream"`
	ActionProfile       string   `yaml:"action_profile"`
	// MaxOutputTokens caps the model's response length per turn. Zero leaves
	// the provider default in place, which is usually far larger than needed
	// and costs decode time on every turn.
	MaxOutputTokens int `yaml:"max_output_tokens"`
}

type ProviderConfig struct {
	Name       string            `yaml:"name"`
	Model      string            `yaml:"model"`
	BaseURL    string            `yaml:"base_url"`
	APIKeyEnv  string            `yaml:"api_key_env"`
	MaxRetries int               `yaml:"max_retries"`
	Headers    map[string]string `yaml:"headers,omitempty"`
}

type ContextConfig struct {
	MaxInputTokens      int     `yaml:"max_input_tokens"`
	ReserveOutputTokens int     `yaml:"reserve_output_tokens"`
	SafetyMargin        float64 `yaml:"safety_margin"`
	// CompactThreshold is the fraction of the input budget that triggers
	// compaction. History below it is left untouched so request prefixes stay
	// stable across turns.
	CompactThreshold float64 `yaml:"compact_threshold"`
	// CompactTarget is the fraction the history is reduced to when compaction
	// runs. It must be lower than CompactThreshold; the gap is the hysteresis
	// that keeps compaction infrequent.
	CompactTarget float64 `yaml:"compact_target"`
}

type SecurityConfig struct {
	DefaultMode                           string `yaml:"default_mode"`
	AllowShell                            bool   `yaml:"allow_shell"`
	AllowNetwork                          bool   `yaml:"allow_network"`
	AllowFileWrite                        bool   `yaml:"allow_file_write"`
	RequireApprovalForDestructiveCommands bool   `yaml:"require_approval_for_destructive_commands"`
	WorkspaceOnly                         bool   `yaml:"workspace_only"`
}

type SessionsConfig struct {
	Directory string `yaml:"directory"`
	AutoSave  bool   `yaml:"auto_save"`
}

type ToolsConfig struct {
	Enabled []string `yaml:"enabled"`
}

type MCPConfig struct {
	Enabled bool `yaml:"enabled"`
}

// UIConfig controls the interactive terminal interface. VELO stays CLI-first:
// the TUI is opt-out and only engages on a real terminal, so piped and
// scripted invocations always use plain output.
type UIConfig struct {
	// Enabled turns the full-screen TUI on when stdout and stdin are TTYs.
	Enabled bool `yaml:"enabled"`
	// Theme selects the color palette: dark, light, or notty (ASCII only).
	Theme string `yaml:"theme"`
}

func Default() Config {
	return Config{
		App: AppConfig{
			Workspace: ".",
			LogLevel:  "info",
		},
		Agent: AgentConfig{
			MaxTurns:            40,
			MaxToolCallsPerTurn: 8,
			RequestTimeout:      Duration(120 * time.Second),
			ToolTimeout:         Duration(60 * time.Second),
			TotalTimeout:        Duration(30 * time.Minute),
			Stream:              true,
			ActionProfile:       "native",
			MaxOutputTokens:     4096,
		},
		Provider: ProviderConfig{
			Name:       "openai-compatible",
			Model:      "example-model",
			BaseURL:    "https://api.example.com/v1",
			APIKeyEnv:  "VELO_API_KEY",
			MaxRetries: 3,
		},
		Context: ContextConfig{
			MaxInputTokens:      64000,
			ReserveOutputTokens: 8000,
			SafetyMargin:        0.10,
			CompactThreshold:    0.80,
			CompactTarget:       0.50,
		},
		Security: SecurityConfig{
			DefaultMode:                           "ask",
			AllowShell:                            false,
			AllowNetwork:                          false,
			AllowFileWrite:                        true,
			RequireApprovalForDestructiveCommands: true,
			WorkspaceOnly:                         true,
		},
		Sessions: SessionsConfig{
			Directory: ".velo/sessions",
			AutoSave:  true,
		},
		Tools: ToolsConfig{
			Enabled: []string{
				"read_file",
				"write_file",
				"apply_patch",
				"list_files",
				"search_files",
				"project_structure",
				"run_command",
				"git_status",
				"git_diff",
			},
		},
		MCP: MCPConfig{
			Enabled: false,
		},
		UI: UIConfig{
			Enabled: true,
			Theme:   "dark",
		},
	}
}
