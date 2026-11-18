package config

import (
	"net/url"
	"strings"
	"testing"
)

func TestRedacted(t *testing.T) {
	cfg := Default()
	cfg.Provider.Headers = map[string]string{
		"Authorization": "Bearer topsecret",
		"X-Api-Key":     "keysecret",
		"X-Apikey":      "apikeysecret",
		"Cookie":        "cookie-value",
		"X-Token":       "token-value",
		"X-Secret":      "secret-value",
		"X-Monkey":      "monkey-value",
		"Content-Type":  "application/json",
		"X-Safe":        "visible",
	}
	cfg.Provider.BaseURL = "https://user:pass@example.com/v1?api_key=query-secret&token=tok-value&model=keep&secret=hidden&safe=yes&API_KEY=upper-secret"

	originalURL := cfg.Provider.BaseURL
	originalSafe := cfg.Provider.Headers["X-Safe"]
	originalAuthorization := cfg.Provider.Headers["Authorization"]

	redacted := cfg.Redacted()

	for _, name := range []string{
		"Authorization",
		"X-Api-Key",
		"X-Apikey",
		"Cookie",
		"X-Token",
		"X-Secret",
		"X-Monkey",
	} {
		if got := redacted.Provider.Headers[name]; got != redactedValue {
			t.Errorf("header %s = %q, want %q", name, got, redactedValue)
		}
	}
	if got := redacted.Provider.Headers["Content-Type"]; got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := redacted.Provider.Headers["X-Safe"]; got != "visible" {
		t.Errorf("X-Safe = %q, want visible", got)
	}

	parsed, err := url.Parse(redacted.Provider.BaseURL)
	if err != nil {
		t.Fatalf("parse redacted base URL: %v", err)
	}
	if parsed.User == nil {
		t.Fatal("redacted base URL lost userinfo")
	}
	if parsed.User.Username() != redactedValue {
		t.Errorf("username = %q, want %q", parsed.User.Username(), redactedValue)
	}
	if password, ok := parsed.User.Password(); !ok || password != redactedValue {
		t.Errorf("password = %q, %v; want %q, true", password, ok, redactedValue)
	}
	query := parsed.Query()
	for _, name := range []string{"api_key", "token", "secret", "API_KEY"} {
		if got := query.Get(name); got != redactedValue {
			t.Errorf("query %s = %q, want %q", name, got, redactedValue)
		}
	}
	if got := query.Get("model"); got != "keep" {
		t.Errorf("query model = %q, want keep", got)
	}
	if got := query.Get("safe"); got != "yes" {
		t.Errorf("query safe = %q, want yes", got)
	}

	if cfg.Provider.Headers["Authorization"] != originalAuthorization {
		t.Errorf("original Authorization changed to %q", cfg.Provider.Headers["Authorization"])
	}
	if cfg.Provider.Headers["X-Safe"] != originalSafe {
		t.Errorf("original X-Safe changed to %q", cfg.Provider.Headers["X-Safe"])
	}
	if cfg.Provider.BaseURL != originalURL {
		t.Errorf("original BaseURL changed to %q", cfg.Provider.BaseURL)
	}

	redacted.Provider.Headers["X-Safe"] = "changed"
	redacted.Provider.Headers["Authorization"] = "changed"
	if cfg.Provider.Headers["X-Safe"] != originalSafe {
		t.Error("redacted header map is not a deep copy")
	}
	if cfg.Provider.Headers["Authorization"] != originalAuthorization {
		t.Error("redacted header map shares values with original")
	}
}

func TestMarshalRedactedDoesNotExposeSecrets(t *testing.T) {
	cfg := Default()
	cfg.Provider.Headers = map[string]string{
		"Authorization": "Bearer topsecret",
		"X-Api-Key":     "keysecret",
	}
	cfg.Provider.BaseURL = "https://user:password@example.com/v1?api_key=query-secret&token=tok-value"

	data, err := cfg.MarshalRedacted()
	if err != nil {
		t.Fatalf("MarshalRedacted() returned error: %v", err)
	}
	output := string(data)
	for _, secret := range []string{
		"topsecret",
		"keysecret",
		"password",
		"query-secret",
		"tok-value",
	} {
		if strings.Contains(output, secret) {
			t.Errorf("MarshalRedacted() output contains secret %q:\n%s", secret, output)
		}
	}
	if !strings.Contains(output, redactedValue) {
		t.Errorf("MarshalRedacted() output does not contain %q:\n%s", redactedValue, output)
	}
}
