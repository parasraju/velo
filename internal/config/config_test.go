package config

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDefault(t *testing.T) {
	want := Config{
		App: AppConfig{
			Workspace: ".",
		},
		Agent: AgentConfig{
			MaxTurns:            40,
			MaxToolCallsPerTurn: 8,
			RequestTimeout:      Duration(120 * time.Second),
			ToolTimeout:         Duration(60 * time.Second),
			TotalTimeout:        Duration(30 * time.Minute),
			Stream:              true,
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
			AllowExecute:                          true,
			AllowShell:                            false,
			AllowNetwork:                          false,
			AllowFileWrite:                        true,
			RequireApprovalForDestructiveCommands: true,
			WorkspaceOnly:                         true,
		},
		Sessions: SessionsConfig{
			Directory: ".velo/sessions",
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
		UI: UIConfig{Enabled: true, Theme: "dark"},
	}

	got := Default()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Default() = %#v, want %#v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Default().Validate() returned error: %v", err)
	}
}

func TestDurationUnmarshalMarshal(t *testing.T) {
	var parsed Duration
	if err := yaml.Unmarshal([]byte("90s"), &parsed); err != nil {
		t.Fatalf("unmarshal duration: %v", err)
	}
	if parsed.Std() != 90*time.Second {
		t.Fatalf("duration = %v, want 90s", parsed.Std())
	}

	data, err := yaml.Marshal(parsed)
	if err != nil {
		t.Fatalf("marshal duration: %v", err)
	}
	var roundTrip Duration
	if err := yaml.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal marshaled duration %q: %v", data, err)
	}
	if roundTrip != parsed {
		t.Fatalf("round trip = %v, want %v", roundTrip, parsed)
	}

	data, err = yaml.Marshal(Duration(0))
	if err != nil {
		t.Fatalf("marshal zero duration: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "0s" {
		t.Fatalf("zero duration marshaled as %q, want 0s", got)
	}

	for _, input := range []string{"120", "true", "soon", "[1s]"} {
		var duration Duration
		if err := yaml.Unmarshal([]byte(input), &duration); err == nil {
			t.Fatalf("unmarshal %q unexpectedly succeeded", input)
		}
	}
}
