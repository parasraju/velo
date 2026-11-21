package openai

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
	providerName  = "openai"
	defaultBase   = "https://api.openai.com/v1"
	maxBodyBytes  = 8 << 20
	maxErrorBytes = 4 << 10
)

type Provider struct {
	model      string
	baseURL    string
	endpoint   string
	apiKey     string
	headers    map[string]string
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

	headers := make(map[string]string, len(opts.Headers))
	for k, v := range opts.Headers {
		headers[k] = v
	}

	return &Provider{
		model:      model,
		baseURL:    baseURL,
		endpoint:   baseURL + "/chat/completions",
		apiKey:     opts.APIKey,
		headers:    headers,
		client:     client,
		maxRetries: maxRetries,
	}, nil
}

func (p *Provider) Name() string {
	return providerName
}

func (p *Provider) Model() string {
	return p.model
}

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Streaming:         true,
		Tools:             true,
		Usage:             true,
		ParallelToolCalls: false,
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
	body, err := p.buildBody(req, false)
	if err != nil {
		return nil, err
	}

	resp, err := p.do(ctx, body)
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
		retryAfter, _ := parseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, protocol.ErrorFromStatus(providerName, resp.StatusCode, truncate(string(data), maxErrorBytes), retryAfter)
	}

	var decoded chatResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, protocol.NewProviderError(protocol.ErrProtocol, providerName, fmt.Sprintf("invalid response json: %v", err))
	}
	if decoded.Error != nil {
		return nil, errorFromChatError(decoded.Error)
	}
	if len(decoded.Choices) == 0 {
		return nil, protocol.NewProviderError(protocol.ErrProtocol, providerName, "response contained no choices")
	}

	choice := decoded.Choices[0]
	text, err := contentText(choice.Message.Content)
	if err != nil {
		return nil, protocol.NewProviderError(protocol.ErrProtocol, providerName, err.Error())
	}

	msg := protocol.Message{Role: protocol.RoleAssistant}
	if text != "" {
		msg.Content = append(msg.Content, protocol.TextContent(text))
	}
	for _, tc := range choice.Message.ToolCalls {
		call, err := convertResponseToolCall(tc)
		if err != nil {
			return nil, err
		}
		msg.Content = append(msg.Content, protocol.ToolCallContent(call))
	}

	out := &protocol.Response{
		Message:      msg,
		FinishReason: mapFinishReason(choice.FinishReason),
		Usage:        convertUsage(decoded.Usage),
	}
	if err := out.Validate(); err != nil {
		return nil, protocol.NewProviderError(protocol.ErrProtocol, providerName, fmt.Sprintf("invalid response: %v", err))
	}
	return out, nil
}

func (p *Provider) Stream(ctx context.Context, req protocol.Request) (provider.Stream, error) {
	body, err := p.buildBody(req, true)
	if err != nil {
		return nil, err
	}

	resp, err := p.do(ctx, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
		if readErr != nil && ctx.Err() != nil {
			return nil, protocol.ErrorFromContext(providerName, ctx.Err())
		}
		retryAfter, _ := parseRetryAfter(resp.Header.Get("Retry-After"))
		message := truncate(string(data), maxErrorBytes)
		if readErr != nil {
			message = readErr.Error()
		}
		return nil, protocol.ErrorFromStatus(providerName, resp.StatusCode, message, retryAfter)
	}
	return newStream(ctx, resp.Body), nil
}

func (p *Provider) do(ctx context.Context, body []byte) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, protocol.ErrorFromContext(providerName, err)
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, fmt.Sprintf("build request: %v", err))
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json")
		if p.apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
		}
		for k, v := range p.headers {
			httpReq.Header.Set(k, v)
		}

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
