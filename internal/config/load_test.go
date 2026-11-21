package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func noEnv(string) (string, bool) {
	return "", false
}

func envLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func writeTestFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	return path
}

func TestLoadMergesPartialConfigOverDefaults(t *testing.T) {
	path := writeTestFile(t, "agent:\n  max_turns: 7\nprovider:\n  headers:\n    X-Custom: value\n")

	got, err := Load(path, noEnv)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	want := Default()
	want.Agent.MaxTurns = 7
	want.Provider.Headers = map[string]string{"X-Custom": "value"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestLoadRejectsRemovedKeys(t *testing.T) {
	// These knobs were removed because nothing honored them. Keeping them in a
	// config would look like a setting and do nothing, so decode rejects them.
	samples := []string{
		"app:\n  log_level: debug\n",
		"agent:\n  action_profile: native\n",
		"sessions:\n  auto_save: true\n",
		"mcp:\n  enabled: false\n",
	}
	for _, content := range samples {
		path := writeTestFile(t, content)
		_, err := Load(path, noEnv)
		if err == nil {
			t.Fatalf("Load() with %q unexpectedly succeeded", content)
		}
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	path := writeTestFile(t, "agent:\n  mystery: 1\n")

	_, err := Load(path, noEnv)
	if err == nil {
		t.Fatal("Load() unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "mystery") {
		t.Fatalf("Load() error = %v, want unknown field name", err)
	}
}

func TestLoadRejectsInvalidDuration(t *testing.T) {
	for _, content := range []string{
		"agent:\n  request_timeout: soon\n",
		"agent:\n  request_timeout: 120\n",
	} {
		path := writeTestFile(t, content)
		_, err := Load(path, noEnv)
		if err == nil {
			t.Fatalf("Load() with %q unexpectedly succeeded", content)
		}
		if !strings.Contains(err.Error(), "duration") {
			t.Fatalf("Load() error = %v, want duration error", err)
		}
	}
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	path := writeTestFile(t, "agent: [\n")

	_, err := Load(path, noEnv)
	if err == nil {
		t.Fatal("Load() unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "decode config") {
		t.Fatalf("Load() error = %v, want decode context", err)
	}
}

func TestLoadRejectsMissingAndEmptyPath(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), noEnv); err == nil {
		t.Fatal("Load() with missing path unexpectedly succeeded")
	}
	if _, err := Load("", noEnv); err == nil {
		t.Fatal("Load() with empty path unexpectedly succeeded")
	}
}

func TestLoadOptional(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	got, found, err := LoadOptional(missing, noEnv)
	if err != nil {
		t.Fatalf("LoadOptional(missing) returned error: %v", err)
	}
	if found {
		t.Fatal("LoadOptional(missing) found = true, want false")
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Fatalf("LoadOptional(missing) = %#v, want defaults", got)
	}

	got, found, err = LoadOptional("", noEnv)
	if err != nil {
		t.Fatalf("LoadOptional(empty) returned error: %v", err)
	}
	if found {
		t.Fatal("LoadOptional(empty) found = true, want false")
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Fatalf("LoadOptional(empty) = %#v, want defaults", got)
	}

	path := writeTestFile(t, "agent:\n  max_turns: 9\n")
	got, found, err = LoadOptional(path, noEnv)
	if err != nil {
		t.Fatalf("LoadOptional(existing) returned error: %v", err)
	}
	if !found {
		t.Fatal("LoadOptional(existing) found = false, want true")
	}
	if got.Agent.MaxTurns != 9 {
		t.Fatalf("LoadOptional(existing).Agent.MaxTurns = %d, want 9", got.Agent.MaxTurns)
	}

	invalidPath := writeTestFile(t, "agent:\n  max_turns: 0\n")
	_, found, err = LoadOptional(invalidPath, noEnv)
	if err == nil {
		t.Fatal("LoadOptional(invalid) unexpectedly succeeded")
	}
	if !found {
		t.Fatal("LoadOptional(invalid) found = false, want true")
	}

	envModel, found, err := LoadOptional(missing, envLookup(map[string]string{"VELO_MODEL": "env-model"}))
	if err != nil {
		t.Fatalf("LoadOptional(missing, env) returned error: %v", err)
	}
	if found {
		t.Fatal("LoadOptional(missing, env) found = true, want false")
	}
	if envModel.Provider.Model != "env-model" {
		t.Fatalf("LoadOptional(missing, env).Provider.Model = %q, want env-model", envModel.Provider.Model)
	}
}

func TestApplyEnvOverrides(t *testing.T) {
	cfg := Default()
	lookup := envLookup(map[string]string{
		"VELO_WORKSPACE":    "workspace",
		"VELO_PROVIDER":     "provider",
		"VELO_MODEL":        "model",
		"VELO_BASE_URL":     "https://example.test/v1",
		"VELO_API_KEY_ENV":  "OTHER_KEY",
		"VELO_SESSIONS_DIR": "sessions",
		"VELO_UNKNOWN":      "ignored",
	})
	if err := cfg.ApplyEnv(lookup); err != nil {
		t.Fatalf("ApplyEnv() returned error: %v", err)
	}

	if cfg.App.Workspace != "workspace" {
		t.Errorf("App.Workspace = %q", cfg.App.Workspace)
	}
	if cfg.Provider.Name != "provider" {
		t.Errorf("Provider.Name = %q", cfg.Provider.Name)
	}
	if cfg.Provider.Model != "model" {
		t.Errorf("Provider.Model = %q", cfg.Provider.Model)
	}
	if cfg.Provider.BaseURL != "https://example.test/v1" {
		t.Errorf("Provider.BaseURL = %q", cfg.Provider.BaseURL)
	}
	if cfg.Provider.APIKeyEnv != "OTHER_KEY" {
		t.Errorf("Provider.APIKeyEnv = %q", cfg.Provider.APIKeyEnv)
	}
	if cfg.Sessions.Directory != "sessions" {
		t.Errorf("Sessions.Directory = %q", cfg.Sessions.Directory)
	}
}

func TestApplyEnvIgnoresEmptyValues(t *testing.T) {
	cfg := Default()
	lookup := envLookup(map[string]string{
		"VELO_WORKSPACE":    "",
		"VELO_MODEL":        "",
		"VELO_SESSIONS_DIR": "",
	})
	if err := cfg.ApplyEnv(lookup); err != nil {
		t.Fatalf("ApplyEnv() returned error: %v", err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Fatalf("ApplyEnv(empty) = %#v, want defaults", cfg)
	}
}

func TestApplyEnvNilUsesProcessEnvironment(t *testing.T) {
	t.Setenv("VELO_MODEL", "from-process")
	cfg := Default()
	if err := cfg.ApplyEnv(nil); err != nil {
		t.Fatalf("ApplyEnv(nil) returned error: %v", err)
	}
	if cfg.Provider.Model != "from-process" {
		t.Fatalf("Provider.Model = %q, want from-process", cfg.Provider.Model)
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	path := writeTestFile(t, "provider:\n  model: file-model\n")

	got, err := Load(path, envLookup(map[string]string{"VELO_MODEL": "env-model"}))
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if got.Provider.Model != "env-model" {
		t.Fatalf("Provider.Model = %q, want env-model", got.Provider.Model)
	}
}
