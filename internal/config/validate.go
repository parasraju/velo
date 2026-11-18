package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	apiKeyEnvPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
	toolNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

func (c Config) Validate() error {
	var errs []error
	add := func(format string, args ...interface{}) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if c.App.Workspace == "" {
		add("app.workspace must not be empty")
	}
	switch c.App.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		add("app.log_level must be one of debug, info, warn, error")
	}

	if c.Agent.MaxTurns <= 0 {
		add("agent.max_turns must be greater than 0")
	}
	if c.Agent.MaxToolCallsPerTurn <= 0 {
		add("agent.max_tool_calls_per_turn must be greater than 0")
	}
	if c.Agent.RequestTimeout <= 0 {
		add("agent.request_timeout must be greater than 0")
	}
	if c.Agent.ToolTimeout <= 0 {
		add("agent.tool_timeout must be greater than 0")
	}
	if c.Agent.TotalTimeout <= 0 {
		add("agent.total_timeout must be greater than 0")
	}
	if c.Agent.MaxOutputTokens < 0 {
		add("agent.max_output_tokens must be greater than or equal to 0")
	}
	switch c.Agent.ActionProfile {
	case "native", "terminal":
	default:
		add("agent.action_profile must be one of native, terminal")
	}

	if c.Provider.Name == "" {
		add("provider.name must not be empty")
	}
	if c.Provider.Model == "" {
		add("provider.model must not be empty")
	}
	if c.Provider.BaseURL != "" {
		parsed, err := url.Parse(c.Provider.BaseURL)
		if err != nil || !isHTTPURL(parsed) {
			add("provider.base_url must be a valid http or https URL with a host")
		}
	}
	if c.Provider.APIKeyEnv != "" && !apiKeyEnvPattern.MatchString(c.Provider.APIKeyEnv) {
		add("provider.api_key_env must be a valid environment variable name")
	}
	if c.Provider.MaxRetries < 0 || c.Provider.MaxRetries > 10 {
		add("provider.max_retries must be between 0 and 10")
	}
	for name := range c.Provider.Headers {
		if name == "" || !headerNamePattern.MatchString(name) {
			add("provider.headers contains invalid header name %q", name)
		}
	}

	if c.Context.MaxInputTokens <= 0 {
		add("context.max_input_tokens must be greater than 0")
	}
	if c.Context.ReserveOutputTokens < 0 {
		add("context.reserve_output_tokens must be greater than or equal to 0")
	}
	if c.Context.ReserveOutputTokens >= c.Context.MaxInputTokens {
		add("context.reserve_output_tokens must be less than context.max_input_tokens")
	}
	if c.Context.SafetyMargin < 0 || c.Context.SafetyMargin >= 1 {
		add("context.safety_margin must be greater than or equal to 0 and less than 1")
	}
	if c.Context.CompactThreshold <= 0 || c.Context.CompactThreshold > 1 {
		add("context.compact_threshold must be greater than 0 and less than or equal to 1")
	}
	if c.Context.CompactTarget <= 0 || c.Context.CompactTarget >= c.Context.CompactThreshold {
		add("context.compact_target must be greater than 0 and less than context.compact_threshold")
	}

	switch c.Security.DefaultMode {
	case "ask", "auto", "deny":
	default:
		add("security.default_mode must be one of ask, auto, deny")
	}

	if c.Sessions.Directory == "" {
		add("sessions.directory must not be empty")
	}

	seenTools := make(map[string]struct{}, len(c.Tools.Enabled))
	for _, tool := range c.Tools.Enabled {
		if tool == "" {
			add("tools.enabled contains an empty tool name")
			continue
		}
		if !toolNamePattern.MatchString(tool) {
			add("tools.enabled contains invalid tool name %q", tool)
		}
		if _, exists := seenTools[tool]; exists {
			add("tools.enabled contains duplicate tool name %q", tool)
		}
		seenTools[tool] = struct{}{}
	}

	switch c.UI.Theme {
	case "dark", "light", "notty":
	default:
		add("ui.theme must be one of dark, light, notty")
	}

	return errors.Join(errs...)
}

func isHTTPURL(parsed *url.URL) bool {
	if parsed == nil || parsed.Host == "" {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return scheme == "http" || scheme == "https"
}
