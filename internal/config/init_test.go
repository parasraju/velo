package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteStarter(t *testing.T) {
	workspace := t.TempDir()

	path, created, err := WriteStarter(workspace)
	if err != nil {
		t.Fatalf("WriteStarter() returned error: %v", err)
	}
	if !created {
		t.Fatal("WriteStarter() created = false, want true")
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("WriteStarter() path = %q, want absolute path", path)
	}

	veloDir := filepath.Join(workspace, ".velo")
	if filepath.Dir(path) != veloDir {
		t.Fatalf("config path directory = %q, want %q", filepath.Dir(path), veloDir)
	}
	for _, dir := range []string{
		veloDir,
		filepath.Join(veloDir, "sessions"),
		filepath.Join(veloDir, "skills"),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat directory %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read starter config: %v", err)
	}
	if !strings.HasPrefix(string(content), "# VELO configuration\n") {
		t.Fatalf("starter config missing header:\n%s", content)
	}

	cfg, err := Load(path, noEnv)
	if err != nil {
		t.Fatalf("Load(starter config) returned error: %v", err)
	}
	if cfg.Agent.MaxTurns != Default().Agent.MaxTurns {
		t.Fatalf("starter Agent.MaxTurns = %d, want %d", cfg.Agent.MaxTurns, Default().Agent.MaxTurns)
	}

	secondPath, secondCreated, err := WriteStarter(workspace)
	if err != nil {
		t.Fatalf("second WriteStarter() returned error: %v", err)
	}
	if secondCreated {
		t.Fatal("second WriteStarter() created = true, want false")
	}
	if secondPath != path {
		t.Fatalf("second WriteStarter() path = %q, want %q", secondPath, path)
	}
	secondContent, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatalf("read starter config after second call: %v", err)
	}
	if string(secondContent) != string(content) {
		t.Fatal("second WriteStarter() modified existing config")
	}
}
