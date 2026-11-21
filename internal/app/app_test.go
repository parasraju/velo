package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"velo/internal/config"
	"velo/internal/provider"
	"velo/internal/provider/mock"
)

func testLookup(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		if value, ok := env[key]; ok {
			return value, true
		}
		if strings.HasPrefix(key, "VELO_") {
			return "", false
		}
		return os.LookupEnv(key)
	}
}

func run(t *testing.T, dir string, env map[string]string, args ...string) (string, string, error) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	a, err := New(Options{
		Version:   "test",
		Stdout:    &stdout,
		Stderr:    &stderr,
		Workspace: dir,
		LookupEnv: testLookup(env),
	})
	if err != nil {
		return stdout.String(), stderr.String(), err
	}

	err = a.Execute(args)
	return stdout.String(), stderr.String(), err
}

func testConfig() config.Config {
	cfg := config.Default()
	cfg.Provider.Name = "mock"
	cfg.Provider.Model = "mock-model"
	cfg.Provider.APIKeyEnv = ""
	return cfg
}

func writeConfig(t *testing.T, dir string, cfg config.Config) string {
	t.Helper()

	path := filepath.Join(dir, ".velo", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestVersion(t *testing.T) {
	dir := t.TempDir()
	stdout, _, err := run(t, dir, nil, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(stdout, "velo test") {
		t.Fatalf("expected version output, got %q", stdout)
	}
	if !strings.Contains(stdout, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Fatalf("expected platform in output, got %q", stdout)
	}
	if !strings.Contains(stdout, "commit: unknown") || !strings.Contains(stdout, "built: unknown") {
		t.Fatalf("expected build metadata, got %q", stdout)
	}

	stdout, _, err = run(t, dir, nil, "--version")
	if err != nil {
		t.Fatalf("--version: %v", err)
	}
	if !strings.Contains(stdout, "velo test") || !strings.Contains(stdout, "commit: unknown") {
		t.Fatalf("expected --version output, got %q", stdout)
	}
}

func TestInit(t *testing.T) {
	dir := t.TempDir()

	stdout, _, err := run(t, dir, nil, "init")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(stdout, "created") {
		t.Fatalf("expected created message, got %q", stdout)
	}

	path := filepath.Join(dir, ".velo", "config.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected config file: %v", err)
	}
	if _, found, err := config.LoadOptional(path, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatalf("load config: %v", err)
	} else if !found {
		t.Fatalf("expected config to parse")
	}

	stdout, _, err = run(t, dir, nil, "init")
	if err != nil {
		t.Fatalf("second init: %v", err)
	}
	if !strings.Contains(stdout, "already exists") {
		t.Fatalf("expected already exists message, got %q", stdout)
	}
}

func TestConfigRedactsSecret(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig()
	const secret = "super-secret-token-value"
	cfg.Provider.Headers = map[string]string{"Authorization": secret}
	writeConfig(t, dir, cfg)

	stdout, _, err := run(t, dir, nil, "config")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if strings.Contains(stdout, secret) {
		t.Fatalf("config output leaked secret: %q", stdout)
	}
	if !strings.Contains(stdout, "Authorization") {
		t.Fatalf("expected header key in output, got %q", stdout)
	}
}

func TestProviders(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, testConfig())

	stdout, _, err := run(t, dir, nil, "providers")
	if err != nil {
		t.Fatalf("providers: %v", err)
	}

	found := false
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "mock" {
			found = true
			if len(fields) < 5 || fields[4] != "yes" {
				t.Fatalf("expected mock to be configured, got %q", line)
			}
		}
	}
	if !found {
		t.Fatalf("expected mock provider, got %q", stdout)
	}
}

func TestProvidersPrintErrorsOutsideTheTable(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, testConfig())
	reg := provider.NewRegistry()
	if err := reg.Register("mock", func(o provider.Options) (provider.Provider, error) {
		return mock.New("mock", o.Model), nil
	}); err != nil {
		t.Fatalf("register mock: %v", err)
	}
	if err := reg.Register("broken", func(provider.Options) (provider.Provider, error) {
		return nil, errors.New("refused")
	}); err != nil {
		t.Fatalf("register broken: %v", err)
	}
	var stdout, stderr bytes.Buffer
	a, err := New(Options{
		Version:   "test",
		Stdout:    &stdout,
		Stderr:    &stderr,
		Workspace: dir,
		Registry:  reg,
		LookupEnv: testLookup(nil),
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if err := a.Execute([]string{"providers"}); err != nil {
		t.Fatalf("providers: %v", err)
	}
	if strings.Contains(stdout.String(), "refused") || strings.Contains(stdout.String(), "unavailable") {
		t.Fatalf("provider error landed in the table:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "NAME") || !strings.Contains(stdout.String(), "mock") {
		t.Fatalf("table missing columns or mock row:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "velo: provider: broken: refused") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestModels(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, testConfig())

	stdout, _, err := run(t, dir, nil, "models")
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if !strings.Contains(stdout, "mock") || !strings.Contains(stdout, "mock-model") {
		t.Fatalf("expected mock and mock-model, got %q", stdout)
	}
}

func TestTools(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig()
	cfg.Tools.Enabled = []string{"read_file", "write_file"}
	writeConfig(t, dir, cfg)

	stdout, _, err := run(t, dir, nil, "tools")
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if !strings.Contains(stdout, "configured tools:") || !strings.Contains(stdout, "read_file") {
		t.Fatalf("expected configured tools, got %q", stdout)
	}
}

func TestStatus(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, testConfig())

	stdout, _, err := run(t, dir, nil, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{dir, "mock-model", "go:", "max output tokens=4096", "version: test"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected %q in status output, got %q", want, stdout)
		}
	}
}

func TestDoctorOK(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, testConfig())

	stdout, _, err := run(t, dir, nil, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	for _, want := range []string{"provider mock", "doctor:"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected %q in doctor output, got %q", want, stdout)
		}
	}
}

func TestDoctorMockSkipsAPIKeyAndDoesNotCreateSessions(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig()
	cfg.Provider.APIKeyEnv = "VELO_API_KEY"
	writeConfig(t, dir, cfg)

	stdout, _, err := run(t, dir, nil, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if strings.Contains(stdout, "VELO_API_KEY is not set") {
		t.Fatalf("mock provider warned about an API key:\n%s", stdout)
	}
	if !strings.Contains(stdout, "api key not required for provider mock") {
		t.Fatalf("expected mock key exemption, got %q", stdout)
	}
	sessions := filepath.Join(dir, ".velo", "sessions")
	if _, statErr := os.Stat(sessions); !os.IsNotExist(statErr) {
		t.Fatalf("doctor created %s: %v", sessions, statErr)
	}
}

func TestDoctorUnknownProvider(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig()
	cfg.Provider.Name = "unknown-provider"
	writeConfig(t, dir, cfg)

	stdout, _, err := run(t, dir, nil, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(stdout, "[warn]") {
		t.Fatalf("expected warning, got %q", stdout)
	}
	if !strings.Contains(stdout, "unknown-provider") {
		t.Fatalf("expected provider name, got %q", stdout)
	}
}

func TestExplicitMissingConfig(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")

	_, _, err := run(t, dir, nil, "config", "--config", missing)
	if err == nil {
		t.Fatalf("expected error for missing explicit config")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("expected path %q in error, got %v", missing, err)
	}
}

func TestUnknownCommand(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := run(t, dir, nil, "nope"); err == nil {
		t.Fatalf("expected error for unknown command")
	}
}

func TestOptionsConfigPath(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, "custom-config.yaml")
	data, err := yaml.Marshal(testConfig())
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(custom, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout bytes.Buffer
	a, err := New(Options{
		Version:    "test",
		Stdout:     &stdout,
		Stderr:     io.Discard,
		Workspace:  dir,
		ConfigPath: custom,
		LookupEnv:  testLookup(nil),
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if err := a.Execute([]string{"models"}); err != nil {
		t.Fatalf("models: %v", err)
	}
	if !strings.Contains(stdout.String(), "mock-model") {
		t.Fatalf("expected config from %s, got %q", custom, stdout.String())
	}
}

func TestConfigWorkspaceApplied(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("create project: %v", err)
	}
	cfg := testConfig()
	cfg.App.Workspace = project
	writeConfig(t, dir, cfg)

	t.Chdir(dir)

	var stdout bytes.Buffer
	a, err := New(Options{
		Version:   "test",
		Stdout:    &stdout,
		Stderr:    io.Discard,
		LookupEnv: testLookup(nil),
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if err := a.Execute([]string{"status"}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(stdout.String(), project) {
		t.Fatalf("expected workspace %s, got %q", project, stdout.String())
	}
}
