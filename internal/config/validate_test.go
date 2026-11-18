package config

import (
	"strings"
	"testing"
)

func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name:   "empty workspace",
			mutate: func(c *Config) { c.App.Workspace = "" },
			want:   "app.workspace",
		},
		{
			name:   "invalid log level",
			mutate: func(c *Config) { c.App.LogLevel = "trace" },
			want:   "app.log_level",
		},
		{
			name:   "zero max turns",
			mutate: func(c *Config) { c.Agent.MaxTurns = 0 },
			want:   "agent.max_turns",
		},
		{
			name:   "zero max tool calls",
			mutate: func(c *Config) { c.Agent.MaxToolCallsPerTurn = 0 },
			want:   "agent.max_tool_calls_per_turn",
		},
		{
			name:   "zero request timeout",
			mutate: func(c *Config) { c.Agent.RequestTimeout = 0 },
			want:   "agent.request_timeout",
		},
		{
			name:   "zero tool timeout",
			mutate: func(c *Config) { c.Agent.ToolTimeout = 0 },
			want:   "agent.tool_timeout",
		},
		{
			name:   "zero total timeout",
			mutate: func(c *Config) { c.Agent.TotalTimeout = 0 },
			want:   "agent.total_timeout",
		},
		{
			name:   "invalid action profile",
			mutate: func(c *Config) { c.Agent.ActionProfile = "other" },
			want:   "agent.action_profile",
		},
		{
			name:   "empty provider name",
			mutate: func(c *Config) { c.Provider.Name = "" },
			want:   "provider.name",
		},
		{
			name:   "empty provider model",
			mutate: func(c *Config) { c.Provider.Model = "" },
			want:   "provider.model",
		},
		{
			name:   "non http base url",
			mutate: func(c *Config) { c.Provider.BaseURL = "ftp://example.com" },
			want:   "provider.base_url",
		},
		{
			name:   "base url without host",
			mutate: func(c *Config) { c.Provider.BaseURL = "https:///v1" },
			want:   "provider.base_url",
		},
		{
			name:   "invalid api key env",
			mutate: func(c *Config) { c.Provider.APIKeyEnv = "1BAD-NAME" },
			want:   "provider.api_key_env",
		},
		{
			name:   "negative max retries",
			mutate: func(c *Config) { c.Provider.MaxRetries = -1 },
			want:   "provider.max_retries",
		},
		{
			name:   "too many max retries",
			mutate: func(c *Config) { c.Provider.MaxRetries = 11 },
			want:   "provider.max_retries",
		},
		{
			name:   "empty header name",
			mutate: func(c *Config) { c.Provider.Headers = map[string]string{"": "value"} },
			want:   "provider.headers",
		},
		{
			name:   "invalid header name",
			mutate: func(c *Config) { c.Provider.Headers = map[string]string{"Bad Header": "value"} },
			want:   "provider.headers",
		},
		{
			name:   "zero max input tokens",
			mutate: func(c *Config) { c.Context.MaxInputTokens = 0 },
			want:   "context.max_input_tokens",
		},
		{
			name:   "negative reserve output tokens",
			mutate: func(c *Config) { c.Context.ReserveOutputTokens = -1 },
			want:   "context.reserve_output_tokens",
		},
		{
			name:   "reserve output tokens equals max input",
			mutate: func(c *Config) { c.Context.ReserveOutputTokens = c.Context.MaxInputTokens },
			want:   "context.reserve_output_tokens",
		},
		{
			name:   "safety margin at one",
			mutate: func(c *Config) { c.Context.SafetyMargin = 1 },
			want:   "context.safety_margin",
		},
		{
			name:   "negative safety margin",
			mutate: func(c *Config) { c.Context.SafetyMargin = -0.1 },
			want:   "context.safety_margin",
		},
		{
			name:   "zero compact threshold",
			mutate: func(c *Config) { c.Context.CompactThreshold = 0 },
			want:   "context.compact_threshold",
		},
		{
			name:   "compact threshold above one",
			mutate: func(c *Config) { c.Context.CompactThreshold = 1.1 },
			want:   "context.compact_threshold",
		},
		{
			name:   "invalid security mode",
			mutate: func(c *Config) { c.Security.DefaultMode = "never" },
			want:   "security.default_mode",
		},
		{
			name:   "empty sessions directory",
			mutate: func(c *Config) { c.Sessions.Directory = "" },
			want:   "sessions.directory",
		},
		{
			name:   "empty tool name",
			mutate: func(c *Config) { c.Tools.Enabled = []string{""} },
			want:   "tools.enabled",
		},
		{
			name:   "invalid tool name",
			mutate: func(c *Config) { c.Tools.Enabled = []string{"ReadFile"} },
			want:   "tools.enabled",
		},
		{
			name:   "duplicate tool name",
			mutate: func(c *Config) { c.Tools.Enabled = []string{"read_file", "read_file"} },
			want:   "tools.enabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want mention of %s", err, tt.want)
			}
		})
	}
}

func TestValidateCollectsAllErrors(t *testing.T) {
	cfg := Default()
	cfg.App.Workspace = ""
	cfg.Agent.MaxTurns = 0
	cfg.Sessions.Directory = ""

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() unexpectedly succeeded")
	}
	for _, field := range []string{"app.workspace", "agent.max_turns", "sessions.directory"} {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("Validate() error = %v, want mention of %s", err, field)
		}
	}
}

func TestValidateBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name: "zero retries",
			mutate: func(c *Config) {
				c.Provider.MaxRetries = 0
			},
		},
		{
			name: "ten retries",
			mutate: func(c *Config) {
				c.Provider.MaxRetries = 10
			},
		},
		{
			name: "zero safety margin",
			mutate: func(c *Config) {
				c.Context.SafetyMargin = 0
			},
		},
		{
			name: "compact threshold at one",
			mutate: func(c *Config) {
				c.Context.CompactThreshold = 1
			},
		},
		{
			name: "small token budget",
			mutate: func(c *Config) {
				c.Context.MaxInputTokens = 1
				c.Context.ReserveOutputTokens = 0
			},
		},
		{
			name: "empty base url",
			mutate: func(c *Config) {
				c.Provider.BaseURL = ""
			},
		},
		{
			name: "empty api key env",
			mutate: func(c *Config) {
				c.Provider.APIKeyEnv = ""
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(&cfg)
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate() returned error: %v", err)
			}
		})
	}
}
