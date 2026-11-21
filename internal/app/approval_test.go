package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"velo/internal/config"
	"velo/internal/tools"
)

func TestApprovalKeyNormalizesRelativePath(t *testing.T) {
	def := tools.Definition{Name: "write_file", Category: tools.CategoryWrite}
	raws := []string{
		`{"path":"a.txt"}`,
		`{"path":"./a.txt"}`,
		`{"path":".\\a.txt"}`,
		`{"path":"sub/../a.txt"}`,
	}
	var first string
	for _, raw := range raws {
		key := approvalKey(def, tools.Call{Arguments: json.RawMessage(raw)})
		if first == "" {
			first = key
			continue
		}
		if key != first {
			t.Fatalf("approval key for %s = %q, want %q", raw, key, first)
		}
	}
	other := approvalKey(def, tools.Call{Arguments: json.RawMessage(`{"path":"b.txt"}`)})
	if other == first {
		t.Fatal("different paths share one trust key")
	}
}

func TestAutoModeRespectsDestructiveApprovalFlag(t *testing.T) {
	def := tools.Definition{Name: "run_command", Category: tools.CategoryExec}
	call := tools.Call{Arguments: json.RawMessage(`{"executable":"rm","args":["-rf","x"]}`)}

	off := &App{cfg: config.Config{Security: config.SecurityConfig{
		DefaultMode:                           "auto",
		RequireApprovalForDestructiveCommands: false,
	}}}
	if err := off.approveTool(context.Background(), def, call); err != nil {
		t.Fatalf("auto mode with the flag off: %v", err)
	}

	on := &App{cfg: config.Config{Security: config.SecurityConfig{
		DefaultMode:                           "auto",
		RequireApprovalForDestructiveCommands: true,
	}}}
	err := on.approveTool(context.Background(), def, call)
	if err == nil || !strings.Contains(err.Error(), "approval required") {
		t.Fatalf("auto mode with the flag on: got %v", err)
	}
}
