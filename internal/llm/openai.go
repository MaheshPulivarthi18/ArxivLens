package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxBodyBytes    = 1 << 20  // a 2xx body over 1 MiB is ErrBadResponse
	maxDiscardBytes = 64 << 10 // read at most this much of a non-2xx body, for connection reuse
	defaultBackoff  = 250 * time.Millisecond
	maxRetryAfter   = 24 * time.Hour // caps a hostile Retry-After so the Duration can't overflow
)

// Client is the OpenAI-compatible Provider (POST {BaseURL}/chat/completions).
// It talks to a local server by default and sends a bearer token only when
// Config.APIKey is set. It is immutable after New and safe for concurrent use.
//
// Complete runs under one deadline, the earlier of the caller's ctx and
// Config.Timeout, covering both attempts and the backoff. It retries at most
// once, only on HTTP 429, 502, 503 and 504, and never sleeps past the deadline.
type Client struct {
	cfg      Config
	endpoint string
	http     *http.Client
	backoff  time.Duration // wait when the server sends no Retry-After; tests shorten it
}

var _ Provider = (*Client)(nil)

// New validates cfg (normalising it first, so a URL holding credentials is
// rejected before anything could print it) and returns a Client.
func New(cfg Config) (*Client, error) {
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &Client{
		cfg:      cfg,
		endpoint: cfg.BaseURL + "/chat/completions",
		http:     &http.Client{Transport: transport}, // no client Timeout: all timing comes from ctx
		backoff:  defaultBackoff,
	}, nil
}

// Name implements Provider.
func (c *Client) Name() string { return "openai-compatible" }

// String prints the redacted config.
func (c *Client) String() string { return "llm.Client{" + c.cfg.String() + "}" }

// GoString is String, so %#v never dumps the key.
func (c *Client) GoString() string { return c.String() }

type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []wireMessage   `json:"messages"`
	MaxTokens      int             `json:"max_tokens"`
	Temperature    float64         `json:"temperature"`
	Stream         bool            `json:"stream"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Complete implements Provider. Every error is an *Error (see the sentinels).
func (c *Client) Complete(ctx context.Context, req Request) (*Response, error) {
	start := time.Now()
	key := c.cfg.APIKey.reveal()
	body, detail := c.encode(req)
	if detail != "" {
		return nil, newError(ErrInvalidRequest, 0, 0, 0, nil, key, detail)
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	for attempt := 1; ; attempt++ {
		resp, e := c.attempt(ctx, body, attempt, key)
		if e == nil {
			resp.Latency = time.Since(start)
			return resp, nil
		}
		if attempt == 2 || !retryable(e.Status) {
			return nil, e
		}
		wait := e.RetryAfter
		if wait == 0 {
			wait = c.backoff
		}
		deadline, _ := ctx.Deadline() // always set by WithTimeout above
		if wait >= time.Until(deadline) {
			// Waiting can't succeed in time: hand the remaining time to the caller's fallback.
			e.detail += "; retry wait exceeds the deadline"
			return nil, e
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctxError(ctx, attempt, e.Status, e.RetryAfter, key)
		}
	}
}

// encode builds the wire body, or returns a non-empty detail for a caller bug.
func (c *Client) encode(req Request) ([]byte, string) {
	if len(req.Messages) == 0 {
		return nil, "no messages"
	}
	if req.MaxTokens < 0 {
		return nil, "negative max tokens"
	}
	wire := chatRequest{Model: c.cfg.Model, MaxTokens: req.MaxTokens, Temperature: req.Temperature}
	if wire.MaxTokens == 0 {
		wire.MaxTokens = c.cfg.MaxTokens
	}
	if req.JSON {
		wire.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	wire.Messages = make([]wireMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		wire.Messages = append(wire.Messages, wireMessage{Role: "system", Content: req.System})
	}
	for i, m := range req.Messages {
		if m.Role != RoleUser && m.Role != RoleAssistant {
			return nil, fmt.Sprintf("message %d has an unsupported role", i)
		}
		wire.Messages = append(wire.Messages, wireMessage{Role: string(m.Role), Content: m.Content})
	}
	body, err := json.Marshal(wire)
	if err != nil { // e.g. a NaN temperature; the json error is dropped so no input is echoed
		return nil, "request does not encode as JSON"
	}
	return body, ""
}

// attempt sends one request. A non-nil *Error with a retryable Status may be retried.
func (c *Client) attempt(ctx context.Context, body []byte, n int, key string) (*Response, *Error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, newError(ErrProvider, 0, n, 0, nil, key, "building the request failed")
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "application/json")
	if key != "" {
		hreq.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := c.http.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctxError(ctx, n, 0, 0, key)
		}
		// Only the inner error is used: never the URL. Headers are never in it.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, newError(ErrProvider, 0, n, 0, err, key, "transport: "+err.Error()+" "+attemptsText(n))
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxDiscardBytes)) // the body is never put in an error
		kind := ErrProvider
		if res.StatusCode == http.StatusTooManyRequests {
			kind = ErrRateLimited
		}
		ra := parseRetryAfter(res.Header.Get("Retry-After"), time.Now())
		return nil, newError(kind, res.StatusCode, n, ra, nil, key, fmt.Sprintf("HTTP %d %s", res.StatusCode, attemptsText(n)))
	}

	data, err := io.ReadAll(io.LimitReader(res.Body, maxBodyBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctxError(ctx, n, res.StatusCode, 0, key)
		}
		return nil, newError(ErrBadResponse, res.StatusCode, n, 0, nil, key, "reading the body failed")
	}
	bad := func(detail string) (*Response, *Error) {
		return nil, newError(ErrBadResponse, res.StatusCode, n, 0, nil, key, detail)
	}
	if len(data) > maxBodyBytes {
		return bad("body exceeds 1 MiB")
	}
	var wire chatResponse
	if err := json.Unmarshal(data, &wire); err != nil { // the json error can quote the body, so it is dropped
		return bad("body is not a chat completion JSON object")
	}
	if len(wire.Choices) == 0 {
		return bad("no choices")
	}
	choice := wire.Choices[0]
	if choice.Message.Content == nil || strings.TrimSpace(*choice.Message.Content) == "" {
		return bad("empty completion")
	}
	out := &Response{Text: *choice.Message.Content, Model: wire.Model, FinishReason: choice.FinishReason}
	if out.Model == "" {
		out.Model = c.cfg.Model
	}
	if wire.Usage != nil { // missing usage stays 0: never estimated
		out.PromptTokens = wire.Usage.PromptTokens
		out.CompletionTokens = wire.Usage.CompletionTokens
	}
	return out, nil
}

// ctxError maps a finished ctx: a passed deadline (ours or the caller's) is
// ErrTimeout; a caller cancel has a nil Kind and the context.Canceled cause.
func ctxError(ctx context.Context, attempts, status int, retryAfter time.Duration, key string) *Error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return newError(ErrTimeout, status, attempts, retryAfter, context.DeadlineExceeded, key, "deadline exceeded "+attemptsText(attempts))
	}
	return newError(nil, status, attempts, retryAfter, context.Canceled, key, "")
}

func retryable(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// parseRetryAfter reads delta-seconds or an HTTP-date. Absent, invalid or past → 0.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n <= 0 {
			return 0
		}
		if n > int64(maxRetryAfter/time.Second) {
			return maxRetryAfter
		}
		return time.Duration(n) * time.Second
	}
	t, err := http.ParseTime(v)
	if err != nil || !t.After(now) {
		return 0
	}
	return min(t.Sub(now), maxRetryAfter)
}
