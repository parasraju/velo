package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"velo/internal/config"
)

type providerPreset struct {
	Model     string
	BaseURL   string
	APIKeyEnv string
}

func presetFor(name string) providerPreset {
	switch name {
	case "mock":
		return providerPreset{Model: "mock-model"}
	case "openai":
		return providerPreset{
			Model:     "gpt-4o-mini",
			BaseURL:   "https://api.openai.com/v1",
			APIKeyEnv: "OPENAI_API_KEY",
		}
	case "openai-compatible":
		return providerPreset{APIKeyEnv: "OPENAI_API_KEY"}
	case "gemini":
		return providerPreset{
			Model:     "gemini-2.0-flash",
			BaseURL:   "https://generativelanguage.googleapis.com/v1beta",
			APIKeyEnv: "GEMINI_API_KEY",
		}
	default:
		return providerPreset{}
	}
}

type providerRow struct {
	Name     string
	Model    string
	Current  bool
	Ready    bool
	NeedsKey bool
	Detail   string
}

func (rt *runtimeEnv) providerRows() []providerRow {
	if rt == nil {
		return nil
	}
	current := rt.app.cfg.Provider.Name
	rt.mu.Lock()
	readyName := ""
	if rt.prov != nil {
		readyName = rt.prov.Name()
	}
	detail := rt.providerErr
	rt.mu.Unlock()

	names := rt.app.registry.Names()
	rows := make([]providerRow, 0, len(names))
	for _, name := range names {
		model := presetFor(name).Model
		if name == current {
			model = rt.app.cfg.Provider.Model
		}
		row := providerRow{
			Name:     name,
			Model:    model,
			Current:  name == current,
			Ready:    name == readyName && readyName != "",
			NeedsKey: providerRequiresAPIKey(name),
		}
		if row.Current && !row.Ready && detail != "" {
			row.Detail = detail
		}
		rows = append(rows, row)
	}
	return rows
}

func (rt *runtimeEnv) formatProviders() string {
	var b strings.Builder
	b.WriteString("providers:\n")
	for _, row := range rt.providerRows() {
		mark := " "
		if row.Current {
			mark = "*"
		}
		state := "available"
		switch {
		case row.Ready:
			state = "connected"
		case row.Current && row.Detail != "":
			state = "not connected"
		case row.NeedsKey:
			state = "needs an API key"
		}
		fmt.Fprintf(&b, " %s %-18s %s\n", mark, row.Name, state)
		if row.Detail != "" {
			fmt.Fprintf(&b, "    %s\n", row.Detail)
		}
	}
	b.WriteString("select with /provider <name>. Choose a model afterwards with /models.\n")
	return b.String()
}

func modelsFor(name string) []string {
	switch name {
	case "mock":
		return []string{"mock-model"}
	case "openai", "openai-compatible":
		return []string{"gpt-4.1", "gpt-4.1-mini", "gpt-4o", "gpt-4o-mini"}
	case "gemini":
		return []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.0-flash", "gemini-2.0-flash-lite"}
	default:
		return nil
	}
}

func (rt *runtimeEnv) modelRows() []providerRow {
	name := strings.TrimSpace(rt.app.cfg.Provider.Name)
	current := rt.app.cfg.Provider.Model
	seen := map[string]struct{}{}
	var rows []providerRow
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" || model == "example-model" {
			return
		}
		if _, ok := seen[model]; ok {
			return
		}
		seen[model] = struct{}{}
		rows = append(rows, providerRow{Name: model, Current: model == current})
	}
	for _, model := range modelsFor(name) {
		add(model)
	}
	add(current)
	return rows
}

func formatModels(rt *runtimeEnv) string {
	rows := rt.modelRows()
	if len(rows) == 0 {
		return "select a provider before choosing a model\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "models for %s:\n", rt.app.cfg.Provider.Name)
	for _, row := range rows {
		mark := " "
		if row.Current {
			mark = "*"
		}
		fmt.Fprintf(&b, " %s %s\n", mark, row.Name)
	}
	b.WriteString("select with /model <name>\n")
	return b.String()
}

func (rt *runtimeEnv) useModel(model string) (string, error) {
	name := strings.TrimSpace(rt.app.cfg.Provider.Name)
	if name == "" || !rt.app.registry.Has(name) {
		return "", fmt.Errorf("select a provider first")
	}
	return rt.useProvider(name, model)
}

// useProvider selects a registered provider and tries to connect it. The API
// key is not written to disk. A missing key leaves the session open.
func (rt *runtimeEnv) useProvider(name, model string) (string, error) {
	name = strings.TrimSpace(name)
	model = strings.TrimSpace(model)
	if name == "" {
		return "", fmt.Errorf("usage: /provider <name> [model]")
	}
	if !rt.app.registry.Has(name) {
		return "", fmt.Errorf("unknown provider %q (%s)", name, strings.Join(rt.app.registry.Names(), ", "))
	}
	prevName := rt.app.cfg.Provider.Name
	preset := presetFor(name)
	rt.app.cfg.Provider.Name = name
	if model != "" {
		rt.app.cfg.Provider.Model = model
	} else if prevName != name || rt.app.cfg.Provider.Model == "" || rt.app.cfg.Provider.Model == "example-model" {
		if preset.Model != "" {
			rt.app.cfg.Provider.Model = preset.Model
		}
	}
	if name == "mock" {
		rt.app.cfg.Provider.BaseURL = ""
		rt.app.cfg.Provider.APIKeyEnv = ""
	} else {
		if preset.BaseURL != "" && (prevName != name || rt.app.cfg.Provider.BaseURL == "" || strings.Contains(rt.app.cfg.Provider.BaseURL, "example.com")) {
			rt.app.cfg.Provider.BaseURL = preset.BaseURL
		}
		if preset.APIKeyEnv != "" && (prevName != name || rt.app.cfg.Provider.APIKeyEnv == "") {
			rt.app.cfg.Provider.APIKeyEnv = preset.APIKeyEnv
		}
	}
	saved := ""
	if err := rt.app.saveProvider(); err != nil {
		saved = " (config not saved: " + err.Error() + ")"
	}
	if err := rt.app.attachProvider(rt); err != nil {
		if providerRequiresAPIKey(name) {
			return fmt.Sprintf("selected %s. Enter the API key to connect.%s", name, saved), nil
		}
		return "", err
	}
	return fmt.Sprintf("connected %s (%s)%s", name, rt.app.cfg.Provider.Model, saved), nil
}

// connectKey keeps the key in this process and connects the current provider.
// The key is not written to the config file and must not be logged.
func (rt *runtimeEnv) connectKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("api key is empty")
	}
	name := strings.TrimSpace(rt.app.cfg.Provider.Name)
	if name == "" || name == "example-model" || !rt.app.registry.Has(name) {
		return "", fmt.Errorf("select a provider first")
	}
	if !providerRequiresAPIKey(name) {
		return fmt.Sprintf("%s does not use an API key", name), nil
	}
	envName := strings.TrimSpace(rt.app.cfg.Provider.APIKeyEnv)
	if envName == "" {
		envName = presetFor(name).APIKeyEnv
		rt.app.cfg.Provider.APIKeyEnv = envName
	}
	if envName == "" {
		return "", fmt.Errorf("provider %s has no api key environment variable", name)
	}
	rt.app.setSecret(envName, key)
	if err := rt.app.attachProvider(rt); err != nil {
		return "", err
	}
	if err := rt.app.saveProvider(); err != nil {
		return fmt.Sprintf("connected %s (config not saved: %s)", name, err.Error()), nil
	}
	return "connected " + name, nil
}

func (a *App) saveProvider() error {
	path := a.configPath
	if path == "" {
		path = filepath.Join(a.workspace, ".velo", "config.yaml")
	}
	return config.UpdateProvider(path, a.cfg.Provider)
}
