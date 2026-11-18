package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

func Load(path string, lookup func(string) (string, bool)) (Config, error) {
	if path == "" {
		return Config{}, errors.New("config path must not be empty")
	}

	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config %s: %w", path, err)
	}
	defer file.Close()

	cfg := Default()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("decode config %s: %w", path, err)
	}
	if err := cfg.ApplyEnv(lookup); err != nil {
		return Config{}, fmt.Errorf("apply environment to config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %s: %w", path, err)
	}
	return cfg, nil
}

func LoadOptional(path string, lookup func(string) (string, bool)) (Config, bool, error) {
	if path == "" {
		cfg, err := defaultWithEnv(lookup)
		return cfg, false, err
	}

	_, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			cfg, cfgErr := defaultWithEnv(lookup)
			return cfg, false, cfgErr
		}
		return Config{}, false, fmt.Errorf("stat config %s: %w", path, err)
	}

	cfg, err := Load(path, lookup)
	return cfg, true, err
}

func defaultWithEnv(lookup func(string) (string, bool)) (Config, error) {
	cfg := Default()
	if err := cfg.ApplyEnv(lookup); err != nil {
		return Config{}, fmt.Errorf("apply environment to default config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate default config: %w", err)
	}
	return cfg, nil
}
