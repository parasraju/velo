package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"velo/internal/protocol"
	"velo/internal/provider"
)

const (
	providerName  = "gemini"
	defaultBase   = "https://generativelanguage.googleapis.com/v1beta"
	maxBodyBytes  = 8 << 20
	maxErrorBytes = 4 << 10
)

// Provider talks to the native Gemini generateContent API.
type Provider struct {
	model      string
	baseURL    string
	apiKey     string
	client     *http.Client
	maxRetries int
}

func New(opts provider.Options) (*Provider, error) {
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, "model is required")
	}

	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		baseURL = defaultBase
	}
	baseURL = strings.TrimRight(baseURL, "/")
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, fmt.Sprintf("invalid base url: %v", err))
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, "base url scheme must be http or https")
	}
	if parsed.Host == "" {
		return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, "base url host is required")
	}
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, protocol.NewProviderError(protocol.ErrAuthentication, providerName, "api key is required")
	}
	if err := provider.CheckMaxRetries(opts.MaxRetries); err != nil {
		return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, err.Error())
	}
	maxRetries := opts.MaxRetries
	if maxRetries == 0 {
		maxRetries = 2
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}

	return &Provider{
		model:      model,
		baseURL:    baseURL,
		apiKey:     opts.APIKey,
		client:     client,
		maxRetries: maxRetries,
	}, nil
}

func (p *Provider) Name() string  { return providerName }
func (p *Provider) Model() string { return p.model }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Streaming:         false,
		Tools:             true,
		ParallelToolCalls: false,
		Usage:             true,
	}
}

func (p *Provider) Close() error {
	if p == nil || p.client == nil {
		return nil
	}
	transport := p.client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
	return nil
}

func (p *Provider) Complete(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	body, model, err := buildBody(req, p.model)
	if err != nil {
		return nil, err
	}

	endpoint := p.baseURL + "/models/" + url.PathEscape(model) + ":generateContent"
	resp, err := p.do(ctx, endpoint, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, protocol.ErrorFromContext(providerName, ctx.Err())
		}
		return nil, protocol.ErrorFromContext(providerName, err)
	}
	if len(data) > maxBodyBytes {
		return nil, protocol.NewProviderError(protocol.ErrProtocol, providerName, "response body exceeded size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errorFromStatus(resp.StatusCode, truncate(string(data), maxErrorBytes))
	}
	return parseResponse(data)
}

// Stream is not supported yet; the agent falls back to Complete because
// Capabilities().Streaming is false.
func (p *Provider) Stream(ctx context.Context, req protocol.Request) (provider.Stream, error) {
	return nil, protocol.NewProviderError(protocol.ErrProtocol, providerName, "streaming is not supported by the gemini provider")
}

func (p *Provider) do(ctx context.Context, endpoint string, body []byte) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, protocol.ErrorFromContext(providerName, err)
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, fmt.Sprintf("build request: %v", err))
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("x-goog-api-key", p.apiKey)

		resp, err := p.client.Do(httpReq)
		if err != nil {
			if ctx.Err() != nil {
				return nil, protocol.ErrorFromContext(providerName, ctx.Err())
			}
			if attempt >= p.maxRetries {
				return nil, protocol.ErrorFromContext(providerName, err)
			}
			if serr := sleepContext(ctx, backoff(attempt, 0, false)); serr != nil {
				return nil, protocol.ErrorFromContext(providerName, serr)
			}
			continue
		}

		if retryableStatus(resp.StatusCode) && attempt < p.maxRetries {
			retryAfter, ok := parseRetryAfter(resp.Header.Get("Retry-After"))
			discardBody(resp.Body)
			if serr := sleepContext(ctx, backoff(attempt, retryAfter, ok)); serr != nil {
				return nil, protocol.ErrorFromContext(providerName, serr)
			}
			continue
		}
		return resp, nil
	}
}

func errorFromStatus(status int, body string) *protocol.ProviderError {
	message := body
	var wrapped struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &wrapped); err == nil && strings.TrimSpace(wrapped.Error.Message) != "" {
		message = wrapped.Error.Message
	}
	return protocol.ErrorFromStatus(providerName, status, message, 0)
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func backoff(attempt int, retryAfter time.Duration, hasRetryAfter bool) time.Duration {
	if hasRetryAfter {
		if retryAfter < 0 {
			return 0
		}
		return retryAfter
	}
	base := 500 * time.Millisecond << uint(attempt)
	jitter := 0.8 + rand.Float64()*0.4
	return time.Duration(float64(base) * jitter)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func discardBody(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4096))
	_ = body.Close()
}

func parseRetryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			seconds = 0
		}
		return time.Duration(seconds) * time.Second, true
	}
	if when, err := http.ParseTime(value); err == nil {
		d := time.Until(when)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return strings.ToValidUTF8(s, "")
	}
	return strings.ToValidUTF8(s[:n], "")
}
