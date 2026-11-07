// Package llm is the provider seam for answer synthesis (ADR-0001, ADR-0002).
//
// Provider is the interface the query service calls. The real implementation
// is an OpenAI-compatible HTTP client that serves a local server (Ollama,
// llama.cpp, vLLM) or a hosted API. Package llmtest has a scripted fake.
//
// The package does no logging. No error, String or GoString output contains
// the API key, the prompt or the completion text.
package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Provider completes a chat request. Implementations must be safe for concurrent use.
type Provider interface {
	Complete(ctx context.Context, req Request) (*Response, error)
	// Name identifies the implementation ("openai-compatible", "fake") for logs and metrics.
	Name() string
}

// Role is the author of a Message. The system prompt goes in Request.System.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one chat turn.
type Message struct {
	Role    Role
	Content string
}

// String prints sizes only, never the content.
func (m Message) String() string {
	return fmt.Sprintf("llm.Message{role: %s, content: %dB}", m.Role, len(m.Content))
}

// GoString is String, so %#v doesn't print the content either.
func (m Message) GoString() string { return m.String() }

// Request is one completion call.
type Request struct {
	System      string    // sent as the first {"role":"system"} message when non-empty
	Messages    []Message // at least one, or ErrInvalidRequest
	MaxTokens   int       // 0 → Config.MaxTokens; < 0 → ErrInvalidRequest
	Temperature float64   // always sent; 0 (deterministic) suits grounded answers
	JSON        bool      // sends response_format {"type":"json_object"}; validating the JSON is the caller's job
}

// String prints sizes only, never the prompt.
func (r Request) String() string {
	return fmt.Sprintf("llm.Request{system: %dB, messages: %d, max_tokens: %d, json: %t}",
		len(r.System), len(r.Messages), r.MaxTokens, r.JSON)
}

// GoString is String, so %#v doesn't print the prompt either.
func (r Request) GoString() string { return r.String() }

// Response is a successful completion.
type Response struct {
	Text             string
	Model            string        // the server's "model"; the configured model if the server omits it
	PromptTokens     int           // 0 when the server omits usage (never estimated)
	CompletionTokens int           // 0 when the server omits usage (never estimated)
	FinishReason     string        // "stop", "length", ...: "length" means the answer was cut off
	Latency          time.Duration // Complete entry to return, retries and backoff included
}

// String prints sizes only, never the completion.
func (r Response) String() string {
	return fmt.Sprintf("llm.Response{text: %dB, model: %s, prompt_tokens: %d, completion_tokens: %d, finish_reason: %s, latency: %s}",
		len(r.Text), r.Model, r.PromptTokens, r.CompletionTokens, r.FinishReason, r.Latency)
}

// GoString is String, so %#v doesn't print the completion either.
func (r Response) GoString() string { return r.String() }

// Sentinel kinds. Every error from a Provider's Complete is an *Error; use errors.Is with these.
var (
	ErrTimeout        = errors.New("llm: timeout")         // our timeout or the caller's deadline passed
	ErrRateLimited    = errors.New("llm: rate limited")    // final status 429
	ErrProvider       = errors.New("llm: provider error")  // any other non-2xx, or a transport error
	ErrBadResponse    = errors.New("llm: bad response")    // unparseable, empty or oversized 2xx body
	ErrInvalidRequest = errors.New("llm: invalid request") // caller bug; nothing was sent
)

// Error is the error type returned by Provider implementations.
//
// When the caller's ctx is cancelled (not past a deadline) Kind is nil and the
// cause is context.Canceled: the client went away, which is not an LLM failure.
type Error struct {
	Kind       error         // one of the sentinels, or nil for a caller cancel
	Status     int           // HTTP status of the last attempt, 0 if none
	Attempts   int           // requests sent: 0, 1 or 2
	RetryAfter time.Duration // parsed from the last response, 0 if absent
	detail     string        // fixed text, statuses, counts; redacted when built
	cause      error         // e.g. context.DeadlineExceeded, a transport error
}

func (e *Error) Error() string {
	msg := "llm: canceled"
	if e.Kind != nil {
		msg = e.Kind.Error()
	}
	if e.detail != "" {
		msg += ": " + e.detail
	}
	return msg
}

// GoString is Error, so %#v never dumps the cause chain.
func (e *Error) GoString() string { return e.Error() }

// Unwrap returns Kind and the cause, leaving out nil entries.
func (e *Error) Unwrap() []error {
	out := make([]error, 0, 2)
	for _, err := range []error{e.Kind, e.cause} {
		if err != nil {
			out = append(out, err)
		}
	}
	return out
}

// newError builds an *Error and strips key (when non-empty) from its text.
func newError(kind error, status, attempts int, retryAfter time.Duration, cause error, key, detail string) *Error {
	if key != "" {
		detail = strings.ReplaceAll(detail, key, "[REDACTED]")
	}
	return &Error{Kind: kind, Status: status, Attempts: attempts, RetryAfter: retryAfter, detail: detail, cause: cause}
}

func attemptsText(n int) string {
	if n == 1 {
		return "after 1 attempt"
	}
	return fmt.Sprintf("after %d attempts", n)
}
