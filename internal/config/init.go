package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func WriteStarter(workspace string) (path string, created bool, err error) {
	baseDir := filepath.Join(workspace, ".velo")
	for _, dir := range []string{
		baseDir,
		filepath.Join(baseDir, "sessions"),
		filepath.Join(baseDir, "skills"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", false, fmt.Errorf("create config directory %s: %w", dir, err)
		}
	}

	configPath := filepath.Join(baseDir, "config.yaml")
	absolutePath, err := filepath.Abs(configPath)
	if err != nil {
		return "", false, fmt.Errorf("resolve config path %s: %w", configPath, err)
	}

	if _, err := os.Stat(absolutePath); err == nil {
		return absolutePath, false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", false, fmt.Errorf("stat config %s: %w", absolutePath, err)
	}

	data, err := yaml.Marshal(Default())
	if err != nil {
		return "", false, fmt.Errorf("marshal default config: %w", err)
	}
	content := append([]byte("# VELO configuration\n"), data...)
	if err := writeFileAtomic(absolutePath, content, 0o644); err != nil {
		return "", false, fmt.Errorf("write starter config %s: %w", absolutePath, err)
	}
	return absolutePath, true, nil
}

func writeFileAtomic(path string, data []byte, perm fs.FileMode) (err error) {
	tempFile, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	tempName := tempFile.Name()
	defer func() {
		if err != nil {
			_ = tempFile.Close()
			_ = os.Remove(tempName)
		}
	}()

	if err = tempFile.Chmod(perm); err != nil {
		return err
	}
	if _, err = tempFile.Write(data); err != nil {
		return err
	}
	if err = tempFile.Sync(); err != nil {
		return err
	}
	if err = tempFile.Close(); err != nil {
		return err
	}
	if err = os.Rename(tempName, path); err != nil {
		return err
	}
	return nil
}
