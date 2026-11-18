package app

import (
	"strings"
	"testing"
)

func TestUnknownEnabledToolFailsRun(t *testing.T) {
	dir := sampleWorkspace(t)
	cfg := testConfig()
	cfg.Tools.Enabled = []string{"read_file", "no_such_tool"}
	cfg.Agent.Stream = false
	writeConfig(t, dir, cfg)

	_, _, err := run(t, dir, nil, "do something")
	if err == nil {
		t.Fatal("expected run to fail for an unknown enabled tool")
	}
	if !strings.Contains(err.Error(), "no_such_tool") {
		t.Fatalf("expected unknown tool in error, got %v", err)
	}
}

func TestToolsCommandListsUnknownToolLeniently(t *testing.T) {
	dir := sampleWorkspace(t)
	cfg := testConfig()
	cfg.Tools.Enabled = []string{"read_file", "no_such_tool"}
	writeConfig(t, dir, cfg)

	stdout, _, err := run(t, dir, nil, "tools")
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if !strings.Contains(stdout, "no_such_tool (not available)") {
		t.Fatalf("expected unknown tool marker, got %q", stdout)
	}
}

func TestDoctorWarnsOnUnknownEnabledTool(t *testing.T) {
	dir := sampleWorkspace(t)
	cfg := testConfig()
	cfg.Tools.Enabled = []string{"read_file", "no_such_tool"}
	writeConfig(t, dir, cfg)

	stdout, _, err := run(t, dir, nil, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(stdout, "warn") || !strings.Contains(stdout, "no_such_tool") {
		t.Fatalf("expected unknown tool warning, got %q", stdout)
	}
}
