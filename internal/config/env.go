package config

import "os"

func (c *Config) ApplyEnv(lookup func(string) (string, bool)) error {
	if lookup == nil {
		lookup = os.LookupEnv
	}

	apply := func(name string, set func(string)) {
		value, ok := lookup(name)
		if ok && value != "" {
			set(value)
		}
	}

	apply("VELO_WORKSPACE", func(value string) { c.App.Workspace = value })
	apply("VELO_PROVIDER", func(value string) { c.Provider.Name = value })
	apply("VELO_MODEL", func(value string) { c.Provider.Model = value })
	apply("VELO_BASE_URL", func(value string) { c.Provider.BaseURL = value })
	apply("VELO_API_KEY_ENV", func(value string) { c.Provider.APIKeyEnv = value })
	apply("VELO_SESSIONS_DIR", func(value string) { c.Sessions.Directory = value })

	return nil
}
