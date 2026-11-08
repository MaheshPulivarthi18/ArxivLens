package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPrintFormsShowSizesOnly(t *testing.T) {
	const prompt, completion = "SENTINEL-PROMPT", "SENTINEL-COMPLETION"
	req := Request{System: prompt, Messages: []Message{{Role: RoleUser, Content: prompt}}, MaxTokens: 7, JSON: true}
	resp := Response{Text: completion, Model: "m", PromptTokens: 1, CompletionTokens: 2, FinishReason: "stop", Latency: time.Second}
	for _, v := range []any{req, &req, req.Messages[0], req.Messages, resp, &resp} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			out := fmt.Sprintf(verb, v)
			if strings.Contains(out, prompt) || strings.Contains(out, completion) {
				t.Fatalf("%s of %T leaked text: %s", verb, v, out)
			}
		}
	}
	if got, want := req.String(), "llm.Request{system: 15B, messages: 1, max_tokens: 7, json: true}"; got != want {
		t.Fatalf("Request.String() = %q, want %q", got, want)
	}
	if got := resp.String(); !strings.Contains(got, "text: 19B") || !strings.Contains(got, "finish_reason: stop") {
		t.Fatalf("Response.String() = %q", got)
	}
}

func TestErrorShape(t *testing.T) {
	cause := errors.New("dial refused")
	tests := []struct {
		name      string
		err       *Error
		wantText  string
		wantChain int
	}{
		{name: "kind and detail", err: newError(ErrProvider, 503, 2, 0, nil, "", "HTTP 503 after 2 attempts"),
			wantText: "llm: provider error: HTTP 503 after 2 attempts", wantChain: 1},
		{name: "kind and cause", err: newError(ErrTimeout, 0, 1, 0, context.DeadlineExceeded, "", "deadline exceeded after 1 attempt"),
			wantText: "llm: timeout: deadline exceeded after 1 attempt", wantChain: 2},
		{name: "cancel has nil kind", err: newError(nil, 0, 1, 0, context.Canceled, "", ""),
			wantText: "llm: canceled", wantChain: 1},
		{name: "zero value", err: &Error{}, wantText: "llm: canceled", wantChain: 0},
		{name: "key redacted from detail", err: newError(ErrProvider, 0, 1, 0, cause, "sk-123", "transport: echo sk-123 and sk-123"),
			wantText: "llm: provider error: transport: echo [REDACTED] and [REDACTED]", wantChain: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.wantText {
				t.Fatalf("Error() = %q, want %q", got, tt.wantText)
			}
			if got := fmt.Sprintf("%#v", tt.err); got != tt.wantText {
				t.Fatalf("%%#v = %q, want %q", got, tt.wantText)
			}
			chain := tt.err.Unwrap()
			if len(chain) != tt.wantChain {
				t.Fatalf("Unwrap() = %v, want %d entries", chain, tt.wantChain)
			}
			for _, e := range chain {
				if e == nil {
					t.Fatal("Unwrap() contains nil")
				}
			}
		})
	}
	if attemptsText(1) != "after 1 attempt" || attemptsText(2) != "after 2 attempts" {
		t.Fatal("attemptsText")
	}
}

func TestErrorsIsAs(t *testing.T) {
	sentinels := []error{ErrTimeout, ErrRateLimited, ErrProvider, ErrBadResponse, ErrInvalidRequest}
	tests := []struct {
		name  string
		err   error // an interface value, the way callers receive it
		kind  error // nil for a caller cancel
		cause error
	}{
		{"timeout", newError(ErrTimeout, 0, 1, 0, context.DeadlineExceeded, "", "deadline"), ErrTimeout, context.DeadlineExceeded},
		{"rate limited", newError(ErrRateLimited, 429, 2, time.Second, nil, "", "HTTP 429"), ErrRateLimited, nil},
		{"provider", newError(ErrProvider, 0, 2, 0, errors.New("dial refused"), "", "transport"), ErrProvider, nil},
		{"bad response", newError(ErrBadResponse, 200, 1, 0, errors.New("bad json"), "", "unparseable body"), ErrBadResponse, nil},
		{"invalid request", newError(ErrInvalidRequest, 0, 0, 0, nil, "", "no messages"), ErrInvalidRequest, nil},
		{"cancel", newError(nil, 0, 1, 0, context.Canceled, "", ""), nil, context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, s := range sentinels {
				if got, want := errors.Is(tt.err, s), s == tt.kind; got != want {
					t.Errorf("errors.Is(err, %v) = %t, want %t", s, got, want)
				}
			}
			if tt.cause != nil && !errors.Is(tt.err, tt.cause) {
				t.Errorf("errors.Is(err, %v) = false, want true", tt.cause)
			}
			if tt.kind != nil && errors.Is(tt.err, context.Canceled) {
				t.Error("errors.Is(err, context.Canceled) = true for a typed error")
			}
			var e *Error
			if !errors.As(tt.err, &e) {
				t.Fatal("errors.As(err, *Error) = false")
			}
			if e.Kind != tt.kind {
				t.Errorf("errors.As Kind = %v, want %v", e.Kind, tt.kind)
			}
		})
	}
}
