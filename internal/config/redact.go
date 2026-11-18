package config

import (
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

const redactedValue = "[REDACTED]"

var sensitiveHeaderParts = []string{
	"authorization",
	"api-key",
	"apikey",
	"cookie",
	"token",
	"secret",
	"key",
}

var sensitiveQueryParams = map[string]struct{}{
	"api_key": {},
	"apikey":  {},
	"key":     {},
	"token":   {},
	"secret":  {},
}

func (c Config) Redacted() Config {
	out := c
	out.Provider.Headers = nil
	if c.Provider.Headers != nil {
		out.Provider.Headers = make(map[string]string, len(c.Provider.Headers))
		for name, value := range c.Provider.Headers {
			if isSensitiveHeader(name) {
				out.Provider.Headers[name] = redactedValue
			} else {
				out.Provider.Headers[name] = value
			}
		}
	}
	out.Provider.BaseURL = redactURL(c.Provider.BaseURL)
	if c.Tools.Enabled != nil {
		out.Tools.Enabled = append([]string(nil), c.Tools.Enabled...)
	}
	return out
}

func (c Config) MarshalRedacted() ([]byte, error) {
	return yaml.Marshal(c.Redacted())
}

func isSensitiveHeader(name string) bool {
	lower := strings.ToLower(name)
	for _, part := range sensitiveHeaderParts {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

func redactURL(raw string) string {
	if raw == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	changed := false
	if parsed.User != nil {
		_, hasPassword := parsed.User.Password()
		if hasPassword {
			parsed.User = url.UserPassword(redactedValue, redactedValue)
		} else {
			parsed.User = url.User(redactedValue)
		}
		changed = true
	}

	query := parsed.Query()
	if len(query) > 0 {
		for name := range query {
			if _, sensitive := sensitiveQueryParams[strings.ToLower(name)]; sensitive {
				query.Set(name, redactedValue)
				changed = true
			}
		}
		if changed {
			parsed.RawQuery = query.Encode()
		}
	}

	if !changed {
		return raw
	}
	return parsed.String()
}
