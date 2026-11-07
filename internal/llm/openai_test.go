package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const slack = 100 * time.Millisecond

var allKinds = []error{ErrTimeout, ErrRateLimited, ErrProvider, ErrBadResponse, ErrInvalidRequest}

const okBody = `{"model":"srv-model","choices":[{"message":{"role":"assistant","content":"hello [1]"},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":11,"completion_tokens":3}}`

// newTestClient points a Client at url with a short default backoff.
func newTestClient(t *testing.T, url, key string, timeout time.Duration) *Client {
	t.Helper()
	c, err := New(Config{BaseURL: url, Model: "cfg-model", APIKey: NewSecret(key), Timeout: timeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.backoff = 10 * time.Millisecond
	return c
}

func userReq(text string) Request {
	return Request{Messages: []Message{{Role: RoleUser, Content: text}}}
}

// script answers the n-th request with steps[n] (the last step repeats) and counts requests.
type script struct {
	mu    sync.Mutex
	steps []func(w http.ResponseWriter, r *http.Request)
	n     int
}

func (s *script) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	step := s.steps[min(s.n, len(s.steps)-1)]
	s.n++
	s.mu.Unlock()
	step(w, r)
}

func (s *script) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func reply(status int, body string, headers ...string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// hang never answers. It drains the body first: net/http only notices a client
// hang-up (and cancels r.Context) once the body is read. The 5 s cap keeps
// srv.Close from blocking a broken test forever.
func hang(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	select {
	case <-r.Context().Done():
	case <-time.After(5 * time.Second):
	}
}

func serve(t *testing.T, steps ...func(http.ResponseWriter, *http.Request)) (*httptest.Server, *script) {
	t.Helper()
	s := &script{steps: steps}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return srv, s
}

func asError(t *testing.T, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want *llm.Error, got %T %v", err, err)
	}
	return e
}

func TestNew(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		wantErr  bool
		noEcho   string
		endpoint string
	}{
		{name: "defaults", cfg: Config{Model: "m"}, endpoint: "http://localhost:11434/v1/chat/completions"},
		{name: "trailing slash", cfg: Config{Model: "m", BaseURL: "https://api.example.com/v1/"}, endpoint: "https://api.example.com/v1/chat/completions"},
		{name: "no model", cfg: Config{BaseURL: "http://h/v1"}, wantErr: true},
		{name: "hand-built userinfo rejected unprinted", cfg: Config{Model: "m", BaseURL: "http://u:SECRETPW@h/v1"}, wantErr: true, noEcho: "SECRETPW"},
		{name: "hand-built query key rejected unprinted", cfg: Config{Model: "m", BaseURL: "https://h/v1?key=SECRETQ"}, wantErr: true, noEcho: "SECRETQ"},
		{name: "negative timeout", cfg: Config{Model: "m", Timeout: -time.Second}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(tt.cfg)
			if tt.wantErr {
				if err == nil || c != nil {
					t.Fatalf("want error and nil client, got %v, %v", c, err)
				}
				if tt.noEcho != "" && strings.Contains(err.Error(), tt.noEcho) {
					t.Fatalf("error echoes the URL: %q", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.endpoint != tt.endpoint || c.Name() != "openai-compatible" || c.backoff != defaultBackoff || c.http.Timeout != 0 {
				t.Fatalf("got endpoint %q name %q backoff %s timeout %s", c.endpoint, c.Name(), c.backoff, c.http.Timeout)
			}
			if c.cfg.Timeout != DefaultTimeout || c.cfg.MaxTokens != DefaultMaxTokens {
				t.Fatalf("defaults not applied: %v", c)
			}
			if got := fmt.Sprintf("%#v", c); !strings.HasPrefix(got, "llm.Client{llm.Config{") {
				t.Fatalf("%%#v = %q", got)
			}
		})
	}
}

func TestCompleteSuccess(t *testing.T) {
	tests := []struct {
		name                  string
		body                  string
		wantModel, wantFinish string
		wantPrompt, wantCompl int
		wantText              string
	}{
		{name: "full response", body: okBody, wantModel: "srv-model", wantFinish: "stop", wantPrompt: 11, wantCompl: 3, wantText: "hello [1]"},
		{name: "missing usage is zero", body: `{"model":"x","choices":[{"message":{"content":"a"},"finish_reason":"length"}]}`,
			wantModel: "x", wantFinish: "length", wantText: "a"},
		{name: "null usage is zero, model falls back to config",
			body: `{"choices":[{"message":{"content":"b"}}],"usage":null}`, wantModel: "cfg-model", wantText: "b"},
		{name: "only the first choice", body: `{"choices":[{"message":{"content":"first"}},{"message":{"content":"second"}}]}`,
			wantModel: "cfg-model", wantText: "first"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, s := serve(t, reply(200, tt.body))
			c := newTestClient(t, srv.URL, "", time.Second)
			resp, err := c.Complete(context.Background(), userReq("q"))
			if err != nil {
				t.Fatal(err)
			}
			if resp.Text != tt.wantText || resp.Model != tt.wantModel || resp.FinishReason != tt.wantFinish ||
				resp.PromptTokens != tt.wantPrompt || resp.CompletionTokens != tt.wantCompl {
				t.Fatalf("got %+v", *resp)
			}
			if resp.Latency <= 0 || s.count() != 1 {
				t.Fatalf("latency %s, requests %d", resp.Latency, s.count())
			}
		})
	}
}

func TestAuthHeader(t *testing.T) {
	for _, key := range []string{"", "sk-test-123"} {
		t.Run(fmt.Sprintf("key=%t", key != ""), func(t *testing.T) {
			var got []string
			var present bool
			srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
				got, present = r.Header.Values("Authorization"), r.Header.Get("Authorization") != ""
				if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Content-Type") != "application/json" {
					w.WriteHeader(418)
					return
				}
				reply(200, okBody)(w, r)
			})
			c := newTestClient(t, srv.URL+"/v1", key, time.Second)
			if _, err := c.Complete(context.Background(), userReq("q")); err != nil {
				t.Fatal(err)
			}
			if key == "" && (present || len(got) != 0) {
				t.Fatalf("Authorization sent without a key: %q", got)
			}
			if key != "" && (len(got) != 1 || got[0] != "Bearer "+key) {
				t.Fatalf("Authorization = %q", got)
			}
		})
	}
}

func TestRequestWire(t *testing.T) {
	type wire struct {
		Model          string            `json:"model"`
		Messages       []map[string]any  `json:"messages"`
		MaxTokens      *int              `json:"max_tokens"`
		Temperature    *float64          `json:"temperature"`
		Stream         *bool             `json:"stream"`
		ResponseFormat map[string]string `json:"response_format"`
	}
	tests := []struct {
		name      string
		req       Request
		wantRoles []string
		wantMax   int
		wantTemp  float64
		wantJSON  bool
	}{
		{name: "default max tokens, no system", req: userReq("hi"), wantRoles: []string{"user"}, wantMax: 256},
		{name: "system first, turns in order, override, json",
			req: Request{System: "sys", Messages: []Message{{RoleUser, "u1"}, {RoleAssistant, "a1"}, {RoleUser, "u2"}},
				MaxTokens: 77, Temperature: 0.3, JSON: true},
			wantRoles: []string{"system", "user", "assistant", "user"}, wantMax: 77, wantTemp: 0.3, wantJSON: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw []byte
			srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
				raw, _ = io.ReadAll(r.Body)
				reply(200, okBody)(w, r)
			})
			c := newTestClient(t, srv.URL, "", time.Second)
			if _, err := c.Complete(context.Background(), tt.req); err != nil {
				t.Fatal(err)
			}
			var got wire
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("body %s: %v", raw, err)
			}
			if got.Model != "cfg-model" || got.MaxTokens == nil || *got.MaxTokens != tt.wantMax ||
				got.Temperature == nil || *got.Temperature != tt.wantTemp || got.Stream == nil || *got.Stream {
				t.Fatalf("body %s", raw)
			}
			if len(got.Messages) != len(tt.wantRoles) {
				t.Fatalf("messages %v, want roles %v", got.Messages, tt.wantRoles)
			}
			for i, role := range tt.wantRoles {
				if got.Messages[i]["role"] != role {
					t.Fatalf("message %d role %v, want %s", i, got.Messages[i]["role"], role)
				}
			}
			if last := got.Messages[len(got.Messages)-1]["content"]; last != tt.req.Messages[len(tt.req.Messages)-1].Content {
				t.Fatalf("last content %v", last)
			}
			if tt.req.System != "" && got.Messages[0]["content"] != tt.req.System {
				t.Fatalf("system content %v", got.Messages[0]["content"])
			}
			hasRF := strings.Contains(string(raw), `"response_format"`)
			if hasRF != tt.wantJSON || (tt.wantJSON && got.ResponseFormat["type"] != "json_object") {
				t.Fatalf("response_format on the wire = %v (%s), want %t", got.ResponseFormat, raw, tt.wantJSON)
			}
		})
	}
}

func TestInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  Request
	}{
		{name: "no messages", req: Request{System: "s"}},
		{name: "negative max tokens", req: Request{Messages: userReq("q").Messages, MaxTokens: -1}},
		{name: "system role in messages", req: Request{Messages: []Message{{Role: "system", Content: "q"}}}},
		{name: "empty role", req: Request{Messages: []Message{{Content: "q"}}}},
		{name: "NaN temperature", req: Request{Messages: userReq("q").Messages, Temperature: math.NaN()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, s := serve(t, reply(200, okBody))
			c := newTestClient(t, srv.URL, "", time.Second)
			_, err := c.Complete(context.Background(), tt.req)
			e := asError(t, err)
			if !errors.Is(err, ErrInvalidRequest) || e.Attempts != 0 || s.count() != 0 {
				t.Fatalf("err %v attempts %d requests %d", err, e.Attempts, s.count())
			}
		})
	}
}

func TestRetry(t *testing.T) {
	future := time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)
	tests := []struct {
		name         string
		steps        []func(http.ResponseWriter, *http.Request)
		timeout      time.Duration
		wantKind     error // nil → success
		wantAttempts int   // requests the server saw
		wantStatus   int
		wantRA       time.Duration
		maxElapsed   time.Duration
	}{
		{name: "429 then 200 retries once", steps: steps(reply(429, "slow down"), reply(200, okBody)), wantAttempts: 2},
		{name: "502 then 200", steps: steps(reply(502, ""), reply(200, okBody)), wantAttempts: 2},
		{name: "504 then 200", steps: steps(reply(504, ""), reply(200, okBody)), wantAttempts: 2},
		{name: "503 twice gives up after 2", steps: steps(reply(503, "")), wantKind: ErrProvider, wantAttempts: 2, wantStatus: 503},
		{name: "429 twice is rate limited", steps: steps(reply(429, "")), wantKind: ErrRateLimited, wantAttempts: 2, wantStatus: 429},
		{name: "400 is not retried", steps: steps(reply(400, "bad")), wantKind: ErrProvider, wantAttempts: 1, wantStatus: 400},
		{name: "500 is not retried", steps: steps(reply(500, "")), wantKind: ErrProvider, wantAttempts: 1, wantStatus: 500},
		{name: "401 is not retried", steps: steps(reply(401, "")), wantKind: ErrProvider, wantAttempts: 1, wantStatus: 401},
		{name: "short Retry-After is honoured", steps: steps(reply(503, "", "Retry-After", "0"), reply(200, okBody)), wantAttempts: 2},
		{name: "Retry-After past the deadline fails now", steps: steps(reply(429, "", "Retry-After", "5")), timeout: 300 * time.Millisecond,
			wantKind: ErrRateLimited, wantAttempts: 1, wantStatus: 429, wantRA: 5 * time.Second, maxElapsed: slack},
		{name: "HTTP-date Retry-After past the deadline", steps: steps(reply(503, "", "Retry-After", future)), timeout: 300 * time.Millisecond,
			wantKind: ErrProvider, wantAttempts: 1, wantStatus: 503, maxElapsed: slack},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, s := serve(t, tt.steps...)
			timeout := tt.timeout
			if timeout == 0 {
				timeout = 2 * time.Second
			}
			c := newTestClient(t, srv.URL, "", timeout)
			start := time.Now()
			resp, err := c.Complete(context.Background(), userReq("q"))
			elapsed := time.Since(start)
			if s.count() != tt.wantAttempts {
				t.Fatalf("server saw %d requests, want %d", s.count(), tt.wantAttempts)
			}
			if elapsed > timeout+slack || (tt.maxElapsed > 0 && elapsed > tt.maxElapsed) {
				t.Fatalf("took %s (timeout %s)", elapsed, timeout)
			}
			if tt.wantKind == nil {
				if err != nil || resp == nil || resp.Text != "hello [1]" {
					t.Fatalf("got %v, %v", resp, err)
				}
				return
			}
			e := asError(t, err)
			if !errors.Is(err, tt.wantKind) || e.Attempts != tt.wantAttempts || e.Status != tt.wantStatus || resp != nil {
				t.Fatalf("got %v (attempts %d, status %d)", err, e.Attempts, e.Status)
			}
			if tt.wantRA != 0 && e.RetryAfter != tt.wantRA {
				t.Fatalf("RetryAfter %s, want %s", e.RetryAfter, tt.wantRA)
			}
		})
	}
}

func steps(s ...func(http.ResponseWriter, *http.Request)) []func(http.ResponseWriter, *http.Request) {
	return s
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", 0}, {"  ", 0}, {"3", 3 * time.Second}, {" 2 ", 2 * time.Second}, {"0", 0}, {"-4", 0}, {"soon", 0},
		{"99999999999999", maxRetryAfter},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0},
		{now.Add(72 * time.Hour).Format(http.TimeFormat), maxRetryAfter},
	}
	for _, tt := range tests {
		if got := parseRetryAfter(tt.in, now); got != tt.want {
			t.Errorf("parseRetryAfter(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestTimeout(t *testing.T) {
	tests := []struct {
		name          string
		cfgTimeout    time.Duration
		callerTimeout time.Duration // 0 → none
		limit         time.Duration // the deadline that should apply
	}{
		{name: "our timeout", cfgTimeout: 200 * time.Millisecond, limit: 200 * time.Millisecond},
		{name: "shorter caller deadline wins", cfgTimeout: 2 * time.Second, callerTimeout: 150 * time.Millisecond, limit: 150 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, s := serve(t, hang)
			c := newTestClient(t, srv.URL, "", tt.cfgTimeout)
			ctx := context.Background()
			if tt.callerTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.callerTimeout)
				defer cancel()
			}
			start := time.Now()
			_, err := c.Complete(ctx, userReq("q"))
			elapsed := time.Since(start)
			if elapsed > tt.limit+slack || elapsed < tt.limit-20*time.Millisecond {
				t.Fatalf("returned after %s, want ≈ %s", elapsed, tt.limit)
			}
			e := asError(t, err)
			if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) || e.Attempts != 1 || s.count() != 1 {
				t.Fatalf("got %v attempts %d", err, e.Attempts)
			}
			for _, k := range allKinds[1:] {
				if errors.Is(err, k) {
					t.Fatalf("timeout also matches %v", k)
				}
			}
		})
	}
}

func TestCancel(t *testing.T) {
	tests := []struct {
		name       string
		steps      []func(http.ResponseWriter, *http.Request)
		cancelAt   time.Duration // < 0: cancel before the call
		wantSeen   int
		wantStatus int
	}{
		{name: "cancel during a 2 s backoff wait", steps: steps(reply(503, "", "Retry-After", "2")), cancelAt: 50 * time.Millisecond, wantSeen: 1, wantStatus: 503},
		{name: "cancel during the request", steps: steps(hang), cancelAt: 50 * time.Millisecond, wantSeen: 1},
		{name: "cancelled before the call", steps: steps(reply(200, okBody)), cancelAt: -1, wantSeen: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, s := serve(t, tt.steps...)
			c := newTestClient(t, srv.URL, "", 5*time.Second)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelAt < 0 {
				cancel()
			} else {
				time.AfterFunc(tt.cancelAt, cancel)
			}
			start := time.Now()
			_, err := c.Complete(ctx, userReq("q"))
			if elapsed := time.Since(start); elapsed > max(tt.cancelAt, 0)+slack {
				t.Fatalf("returned after %s", elapsed)
			}
			e := asError(t, err)
			if !errors.Is(err, context.Canceled) || e.Kind != nil || err.Error() != "llm: canceled" {
				t.Fatalf("got %q kind %v", err, e.Kind)
			}
			for _, k := range allKinds {
				if errors.Is(err, k) {
					t.Fatalf("cancel matches sentinel %v", k)
				}
			}
			if errors.Is(err, context.DeadlineExceeded) || s.count() != tt.wantSeen || e.Status != tt.wantStatus {
				t.Fatalf("deadline match, or requests %d status %d", s.count(), e.Status)
			}
		})
	}
}

func TestBadResponse(t *testing.T) {
	huge := `{"choices":[{"message":{"content":"` + strings.Repeat("x", maxBodyBytes) + `"}}]}`
	tests := []struct {
		name   string
		body   string
		detail string
	}{
		{name: "not JSON", body: "<html>gateway</html>", detail: "not a chat completion"},
		{name: "wrong JSON type", body: `{"choices":"nope"}`, detail: "not a chat completion"},
		{name: "empty choices", body: `{"choices":[]}`, detail: "no choices"},
		{name: "no choices field", body: `{}`, detail: "no choices"},
		{name: "null content", body: `{"choices":[{"message":{"content":null}}]}`, detail: "empty completion"},
		{name: "whitespace content", body: `{"choices":[{"message":{"content":" \n "}}]}`, detail: "empty completion"},
		{name: "oversized body", body: huge, detail: "exceeds 1 MiB"},
		{name: "empty body", body: "", detail: "not a chat completion"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, s := serve(t, reply(200, tt.body))
			c := newTestClient(t, srv.URL, "", 2*time.Second)
			resp, err := c.Complete(context.Background(), userReq("q"))
			e := asError(t, err)
			if !errors.Is(err, ErrBadResponse) || resp != nil || e.Attempts != 1 || s.count() != 1 || e.Status != 200 {
				t.Fatalf("got %v, attempts %d, requests %d", err, e.Attempts, s.count())
			}
			if !strings.Contains(err.Error(), tt.detail) {
				t.Fatalf("error %q, want %q", err, tt.detail)
			}
		})
	}
}

func TestBodyCapBoundary(t *testing.T) {
	prefix, suffix := `{"choices":[{"message":{"content":"`, `"}}]}`
	body := prefix + strings.Repeat("y", maxBodyBytes-len(prefix)-len(suffix)) + suffix
	srv, _ := serve(t, reply(200, body))
	c := newTestClient(t, srv.URL, "", 2*time.Second)
	resp, err := c.Complete(context.Background(), userReq("q"))
	if err != nil || len(body) != maxBodyBytes || len(resp.Text) != maxBodyBytes-len(prefix)-len(suffix) {
		t.Fatalf("exactly 1 MiB should parse: %v", err)
	}
}

func TestTransportError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // connection refused
	c := newTestClient(t, url, "", time.Second)
	start := time.Now()
	_, err := c.Complete(context.Background(), userReq("q"))
	e := asError(t, err)
	if !errors.Is(err, ErrProvider) || e.Attempts != 1 || e.Status != 0 || time.Since(start) > time.Second {
		t.Fatalf("got %v attempts %d", err, e.Attempts)
	}
	if strings.Contains(err.Error(), url) || !strings.Contains(err.Error(), "transport:") {
		t.Fatalf("error %q should name the transport failure, not the URL", err)
	}
}

func TestConcurrentComplete(t *testing.T) {
	var hits atomic.Int64
	srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1)%3 == 0 {
			reply(503, "")(w, r)
			return
		}
		reply(200, okBody)(w, r)
	})
	c := newTestClient(t, srv.URL, "k", 2*time.Second)
	var wg sync.WaitGroup
	errs := make(chan error, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Complete(context.Background(), userReq("q")); err != nil && !errors.Is(err, ErrProvider) {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestNoSecretLeaks: a hostile server echoes the Authorization header and the
// request body into every response; no sentinel may reach any error text.
func TestNoSecretLeaks(t *testing.T) {
	const (
		key        = "sk-SENTINEL-KEY-0123456789"
		prompt     = "SENTINEL-PROMPT"
		completion = "SENTINEL-COMPLETION"
	)
	sentinels := []string{key, prompt, completion}
	echo := func(status int, extra string, headers ...string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+key {
				w.WriteHeader(418) // the test must not pass by never sending the key
				return
			}
			body, _ := io.ReadAll(r.Body)
			for i := 0; i+1 < len(headers); i += 2 {
				w.Header().Set(headers[i], headers[i+1])
			}
			w.Header().Set("X-Echo", r.Header.Get("Authorization"))
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, "%s %s %s %s", r.Header.Get("Authorization"), body, completion, extra)
		}
	}
	bigJSON := `{"choices":[{"message":{"content":"` + completion + strings.Repeat("z", maxBodyBytes) + `"}}]}`
	tests := []struct {
		name    string
		steps   []func(http.ResponseWriter, *http.Request)
		timeout time.Duration
		cancel  bool
		closed  bool
		kind    error // nil → cancel
	}{
		{name: "429 twice", steps: steps(echo(429, "")), kind: ErrRateLimited},
		{name: "503 twice", steps: steps(echo(503, "")), kind: ErrProvider},
		{name: "400", steps: steps(echo(400, "")), kind: ErrProvider},
		{name: "Retry-After past the deadline", steps: steps(echo(429, "", "Retry-After", "30")), kind: ErrRateLimited},
		{name: "hang", steps: steps(func(w http.ResponseWriter, r *http.Request) { hang(w, r) }), timeout: 100 * time.Millisecond, kind: ErrTimeout},
		{name: "non-JSON", steps: steps(echo(200, "")), kind: ErrBadResponse},
		{name: "empty choices", steps: steps(func(w http.ResponseWriter, r *http.Request) {
			reply(200, `{"choices":[],"note":"`+key+completion+`"}`)(w, r)
		}), kind: ErrBadResponse},
		{name: "oversized", steps: steps(echo(200, bigJSON)), kind: ErrBadResponse},
		{name: "connection refused", closed: true, kind: ErrProvider},
		{name: "cancel during backoff", steps: steps(echo(503, "", "Retry-After", "1")), cancel: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var url string
			if tt.closed {
				srv := httptest.NewServer(http.NotFoundHandler())
				url = srv.URL
				srv.Close()
			} else {
				srv, _ := serve(t, tt.steps...)
				url = srv.URL
			}
			timeout := tt.timeout
			if timeout == 0 {
				timeout = 2 * time.Second
			}
			c := newTestClient(t, url, key, timeout)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancel {
				time.AfterFunc(50*time.Millisecond, cancel)
			}
			req := Request{System: "system " + prompt, Messages: []Message{{Role: RoleUser, Content: prompt}}}
			_, err := c.Complete(ctx, req)
			if err == nil {
				t.Fatal("want an error")
			}
			if tt.kind != nil && !errors.Is(err, tt.kind) {
				t.Fatalf("got %v, want %v", err, tt.kind)
			}
			if tt.kind == nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v, want cancel", err)
			}
			outs := []string{err.Error()}
			for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
				outs = append(outs, fmt.Sprintf(verb, err), fmt.Sprintf(verb, c), fmt.Sprintf(verb, req))
			}
			for _, out := range outs {
				for _, s := range sentinels {
					if strings.Contains(out, s) {
						t.Fatalf("%q leaked in %q", s, out)
					}
				}
			}
		})
	}
}
