package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arxivlens/internal/db"
)

// TestWriteJSON pins RET-1c: an unencodable response is a logged 500, never an
// empty 200, and a successful body is byte-identical to json.Encoder output.
func TestWriteJSON(t *testing.T) {
	type okBody struct {
		Q string `json:"q"`
		N int    `json:"n"`
	}

	// encoderBytes is what the handlers wrote before RET-1c.
	encoderBytes := func(t *testing.T, v any) string {
		t.Helper()
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(v); err != nil {
			t.Fatalf("reference encode: %v", err)
		}
		return buf.String()
	}

	tests := []struct {
		name    string
		status  int
		value   any
		route   string
		traceID string

		wantCode int
		wantJSON bool   // Content-Type is application/json and body is the encoder's bytes
		wantBody string // exact body when non-empty (overrides the encoder reference)
		wantLogs []string
	}{
		{
			name:     "unencodable",
			status:   http.StatusOK,
			value:    map[string]float64{"x": math.NaN()},
			route:    "/v1/search",
			wantCode: http.StatusInternalServerError,
			wantLogs: []string{"encode response failed route=/v1/search", "trace_id=-", "unsupported value"},
		},
		{
			name:     "unencodable with trace ID",
			status:   http.StatusOK,
			value:    map[string]float64{"x": math.Inf(1)},
			route:    "/v1/ask",
			traceID:  "t-ret1c",
			wantCode: http.StatusInternalServerError,
			wantLogs: []string{"encode response failed route=/v1/ask", "trace_id=t-ret1c", "unsupported value"},
		},
		{
			name:     "ok",
			status:   http.StatusOK,
			value:    okBody{Q: "a<b & c>d", N: 1},
			route:    "/v1/search",
			wantCode: http.StatusOK,
			wantJSON: true,
		},
		{
			name:     "status passthrough",
			status:   http.StatusInternalServerError,
			value:    map[string]string{"error": "boom"},
			route:    "/v1/papers/ingest",
			wantCode: http.StatusInternalServerError,
			wantJSON: true,
			wantBody: "{\"error\":\"boom\"}\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logBuf := redirectServerLog(t)

			rec := httptest.NewRecorder()
			writeJSON(rec, tc.status, tc.value, tc.route, tc.traceID)

			if rec.Code != tc.wantCode {
				t.Errorf("code = %d, want %d", rec.Code, tc.wantCode)
			}
			ct := rec.Header().Get("Content-Type")
			body := rec.Body.String()
			logLines := nonEmptyLines(logBuf.String())

			if tc.wantJSON {
				if ct != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}
				want := tc.wantBody
				if want == "" {
					want = encoderBytes(t, tc.value)
				}
				if body != want {
					t.Errorf("body = %q, want %q", body, want)
				}
			} else {
				if ct == "application/json" {
					t.Errorf("Content-Type = application/json on an encoding failure")
				}
				if strings.HasPrefix(body, "{") {
					t.Errorf("body starts with '{' on an encoding failure: %q", body)
				}
			}

			if len(tc.wantLogs) == 0 {
				if len(logLines) != 0 {
					t.Errorf("unexpected log lines: %q", logLines)
				}
				return
			}
			if len(logLines) != 1 {
				t.Fatalf("got %d log lines, want 1: %q", len(logLines), logLines)
			}
			for _, sub := range tc.wantLogs {
				if !strings.Contains(logLines[0], sub) {
					t.Errorf("log line %q does not contain %q", logLines[0], sub)
				}
			}
		})
	}
}

// redirectServerLog sends the standard logger to a buffer for the test and restores the
// previous writer and flags afterwards.
func redirectServerLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })
	return &buf
}

// fakeDeps stands in for all four Server dependencies with canned results.
type fakeDeps struct {
	hits     []SearchHit
	ask      *AskResponse
	paper    *db.PaperRecord
	stats    *db.SystemStats
	ingest   *IngestResult
	discover *IngestResult
	err      error // returned by every method when set
}

func (f *fakeDeps) HybridSearch(context.Context, string, []string, int, bool) ([]SearchHit, error) {
	return f.hits, f.err
}

func (f *fakeDeps) AskWith(context.Context, string, []string, int, AskOptions) (*AskResponse, error) {
	return f.ask, f.err
}

func (f *fakeDeps) GetPaperStatus(context.Context, string) (*db.PaperRecord, error) {
	return f.paper, f.err
}

func (f *fakeDeps) GetSystemStats(context.Context) (*db.SystemStats, error) {
	return f.stats, f.err
}

func (f *fakeDeps) IngestPaper(context.Context, string) (*IngestResult, error) {
	return f.ingest, f.err
}

func (f *fakeDeps) DiscoverAndIngest(context.Context, string) (*IngestResult, error) {
	return f.discover, f.err
}

// TestHandlersEncodeContract pins RET-1d: each handler passes the right status,
// value, route label and trace ID to writeJSON, and an unconfigured ingester is a 503.
func TestHandlersEncodeContract(t *testing.T) {
	// time.Time.MarshalJSON fails outside years [0,9999]: the unencodable value for
	// the db types, which have no float fields.
	badTime := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	goodTime := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

	hits := []SearchHit{{ChunkID: 7, ArxivID: "2401.00001", Title: "a<b & c>d", RRFScore: 0.5}}
	nanHits := []SearchHit{{ChunkID: 7, ArxivID: "2401.00001", RRFScore: math.NaN()}}
	askOK := &AskResponse{Answer: "x <y> [1]", TraceID: "t-ret1d", Citations: []Citation{{Marker: 1, Score: 0.9, Status: StatusSupported}}}
	askNaN := &AskResponse{Answer: "x", TraceID: "t-ret1d", Citations: []Citation{{Marker: 1, Score: math.NaN()}}}
	paperOK := &db.PaperRecord{ArxivID: "2401.00001", Title: "a<b", Status: "embedded", PublishedAt: goodTime, CreatedAt: goodTime}
	paperBad := &db.PaperRecord{ArxivID: "2401.00001", PublishedAt: badTime}
	statsOK := &db.SystemStats{TotalPapers: 2, Papers: []*db.PaperRecord{paperOK}}
	statsBad := &db.SystemStats{TotalPapers: 1, Papers: []*db.PaperRecord{paperBad}}
	ingestOK := &IngestResult{Status: "embedded", ArxivID: "2401.00001", Title: "a<b", Chunks: 3}
	discoverOK := &IngestResult{Status: "embedded", ArxivID: "2402.00002", Title: "c&d", Chunks: 4}

	type handler func(*Server) http.HandlerFunc
	search := func(s *Server) http.HandlerFunc { return s.handleSearch }
	ask := func(s *Server) http.HandlerFunc { return s.handleAsk }
	status := func(s *Server) http.HandlerFunc { return s.handlePaperStatus }
	stats := func(s *Server) http.HandlerFunc { return s.handleStats }
	ingest := func(s *Server) http.HandlerFunc { return s.handleIngestPaper }
	discover := func(s *Server) http.HandlerFunc { return s.handleDiscoverPaper }

	withFake := func(f *fakeDeps) func() *Server {
		return func() *Server {
			return &Server{searchEngine: f, ragService: f, database: f, ingester: f, port: 8080}
		}
	}
	neverSet := func() *Server { return NewServer(nil, nil, nil, 0) }
	setNil := func() *Server {
		s := NewServer(nil, nil, nil, 0)
		s.SetIngester(nil)
		return s
	}
	boom := errors.New("boom")
	const notConfigured = "paper ingester not configured on server\n"

	tests := []struct {
		name    string
		server  func() *Server
		handler handler
		method  string
		path    string
		body    string

		wantCode int
		wantJSON any    // when non-nil: Content-Type application/json and body == json.Encoder(wantJSON)
		wantBody string // exact body (checked when non-empty)
		wantLogs []string
	}{
		// /v1/search
		{name: "search ok", server: withFake(&fakeDeps{hits: hits}), handler: search, method: http.MethodPost, path: "/v1/search",
			body: `{"query":"a<b"}`, wantCode: http.StatusOK,
			wantJSON: map[string]interface{}{"query": "a<b", "count": len(hits), "hits": hits}},
		{name: "search unencodable", server: withFake(&fakeDeps{hits: nanHits}), handler: search, method: http.MethodPost, path: "/v1/search",
			body: `{"query":"q"}`, wantCode: http.StatusInternalServerError,
			wantLogs: []string{"route=/v1/search ", "trace_id=-"}},

		// /v1/ask
		{name: "ask ok", server: withFake(&fakeDeps{ask: askOK}), handler: ask, method: http.MethodPost, path: "/v1/ask",
			body: `{"question":"q"}`, wantCode: http.StatusOK, wantJSON: askOK},
		{name: "ask unencodable", server: withFake(&fakeDeps{ask: askNaN}), handler: ask, method: http.MethodPost, path: "/v1/ask",
			body: `{"question":"q"}`, wantCode: http.StatusInternalServerError,
			wantLogs: []string{"route=/v1/ask ", "trace_id=t-ret1d:"}},

		// /v1/papers/{arxiv_id}/status
		{name: "status ok", server: withFake(&fakeDeps{paper: paperOK}), handler: status, method: http.MethodGet,
			path: "/v1/papers/2401.00001/status", wantCode: http.StatusOK, wantJSON: paperOK},
		{name: "status unencodable", server: withFake(&fakeDeps{paper: paperBad}), handler: status, method: http.MethodGet,
			path: "/v1/papers/2401.00001/status", wantCode: http.StatusInternalServerError,
			wantLogs: []string{"route=/v1/papers/{arxiv_id}/status ", "trace_id=-"}},

		// /v1/stats
		{name: "stats ok", server: withFake(&fakeDeps{stats: statsOK}), handler: stats, method: http.MethodGet, path: "/v1/stats",
			wantCode: http.StatusOK, wantJSON: statsOK},
		{name: "stats unencodable", server: withFake(&fakeDeps{stats: statsBad}), handler: stats, method: http.MethodGet, path: "/v1/stats",
			wantCode: http.StatusInternalServerError, wantLogs: []string{"route=/v1/stats ", "trace_id=-"}},

		// /v1/papers/ingest. *IngestResult has only string and int fields, so json.Marshal
		// cannot fail for it: there is no unencodable row for ingest or discover.
		{name: "ingest ok", server: withFake(&fakeDeps{ingest: ingestOK}), handler: ingest, method: http.MethodPost,
			path: "/v1/papers/ingest", body: `{"arxiv_id":"2401.00001"}`, wantCode: http.StatusOK, wantJSON: ingestOK},
		{name: "ingest error", server: withFake(&fakeDeps{err: boom}), handler: ingest, method: http.MethodPost,
			path: "/v1/papers/ingest", body: `{"arxiv_id":"2401.00001"}`, wantCode: http.StatusInternalServerError,
			wantJSON: map[string]string{"error": "boom"}, wantBody: "{\"error\":\"boom\"}\n"},
		{name: "ingest never set", server: neverSet, handler: ingest, method: http.MethodPost, path: "/v1/papers/ingest",
			body: `{"arxiv_id":"2401.00001"}`, wantCode: http.StatusServiceUnavailable, wantBody: notConfigured},
		{name: "ingest set nil", server: setNil, handler: ingest, method: http.MethodPost, path: "/v1/papers/ingest",
			body: `{"arxiv_id":"2401.00001"}`, wantCode: http.StatusServiceUnavailable, wantBody: notConfigured},

		// /v1/papers/discover
		{name: "discover ok", server: withFake(&fakeDeps{discover: discoverOK}), handler: discover, method: http.MethodPost,
			path: "/v1/papers/discover", body: `{"query":"q"}`, wantCode: http.StatusOK, wantJSON: discoverOK},
		{name: "discover error", server: withFake(&fakeDeps{err: boom}), handler: discover, method: http.MethodPost,
			path: "/v1/papers/discover", body: `{"query":"q"}`, wantCode: http.StatusInternalServerError,
			wantJSON: map[string]string{"error": "boom"}, wantBody: "{\"error\":\"boom\"}\n"},
		{name: "discover never set", server: neverSet, handler: discover, method: http.MethodPost, path: "/v1/papers/discover",
			body: `{"query":"q"}`, wantCode: http.StatusServiceUnavailable, wantBody: notConfigured},
		{name: "discover set nil", server: setNil, handler: discover, method: http.MethodPost, path: "/v1/papers/discover",
			body: `{"query":"q"}`, wantCode: http.StatusServiceUnavailable, wantBody: notConfigured},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logBuf := redirectServerLog(t)

			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			// A mutant can turn a nil-ingester 503 into a nil-pointer call; fail the
			// row instead of letting the panic stop every later test.
			if err := func() (err error) {
				defer func() {
					if r := recover(); r != nil {
						err = fmt.Errorf("handler panicked: %v", r)
					}
				}()
				tc.handler(tc.server())(rec, req)
				return nil
			}(); err != nil {
				t.Fatal(err)
			}

			if rec.Code != tc.wantCode {
				t.Errorf("code = %d, want %d", rec.Code, tc.wantCode)
			}
			ct := rec.Header().Get("Content-Type")
			body := rec.Body.String()
			logLines := nonEmptyLines(logBuf.String())

			if tc.wantJSON != nil {
				if ct != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}
				var ref bytes.Buffer
				if err := json.NewEncoder(&ref).Encode(tc.wantJSON); err != nil {
					t.Fatalf("reference encode: %v", err)
				}
				if body != ref.String() {
					t.Errorf("body = %q, want %q", body, ref.String())
				}
			} else if ct == "application/json" {
				t.Errorf("Content-Type = application/json, want a non-JSON error")
			}
			if tc.wantBody != "" && body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}

			if len(tc.wantLogs) == 0 {
				if len(logLines) != 0 {
					t.Errorf("unexpected log lines: %q", logLines)
				}
				return
			}
			if len(logLines) != 1 {
				t.Fatalf("got %d log lines, want 1: %q", len(logLines), logLines)
			}
			for _, sub := range append([]string{"encode response failed"}, tc.wantLogs...) {
				if !strings.Contains(logLines[0], sub) {
					t.Errorf("log line %q does not contain %q", logLines[0], sub)
				}
			}
		})
	}
}

// TestHandlersErrorContract pins RET-1e: every http.Error path in server.go answers
// its own status as text/plain with a known body prefix, never a JSON success. It
// also pins that SetIngester keeps a non-nil ingester (mutant q2).
func TestHandlersErrorContract(t *testing.T) {
	type handler func(*Server) http.HandlerFunc
	search := func(s *Server) http.HandlerFunc { return s.handleSearch }
	ask := func(s *Server) http.HandlerFunc { return s.handleAsk }
	status := func(s *Server) http.HandlerFunc { return s.handlePaperStatus }
	stats := func(s *Server) http.HandlerFunc { return s.handleStats }
	ingest := func(s *Server) http.HandlerFunc { return s.handleIngestPaper }
	discover := func(s *Server) http.HandlerFunc { return s.handleDiscoverPaper }

	withFake := func(f *fakeDeps) func() *Server {
		return func() *Server { return &Server{searchEngine: f, ragService: f, database: f, ingester: f, port: 8080} }
	}
	ok := withFake(&fakeDeps{})
	failing := withFake(&fakeDeps{err: errors.New("boom")})
	noIngester := func() *Server { return NewServer(nil, nil, nil, 0) }
	const post, get = http.MethodPost, http.MethodGet
	const notConfigured = "paper ingester not configured on server\n"

	tests := []struct {
		name     string
		server   func() *Server
		handler  handler
		method   string
		path     string
		body     string
		wantCode int
		wantBody string // body prefix
	}{
		{"search bad JSON", ok, search, post, "/v1/search", `{`, 400, "invalid request payload: "},
		{"search empty query", ok, search, post, "/v1/search", `{"query":""}`, 400, "query is required\n"},
		{"search error", failing, search, post, "/v1/search", `{"query":"q"}`, 500, "search failed: boom\n"},
		{"ask bad JSON", ok, ask, post, "/v1/ask", `{`, 400, "invalid request payload: "},
		{"ask empty question", ok, ask, post, "/v1/ask", `{"question":""}`, 400, "question is required\n"},
		{"ask error", failing, ask, post, "/v1/ask", `{"question":"q"}`, 500, "rag ask failed: boom\n"},
		{"status empty arxiv_id", ok, status, get, "/v1/papers/", "", 400, "arxiv_id is required\n"},
		{"status error", failing, status, get, "/v1/papers/2401.00001/status", "", 404, "paper not found: boom\n"},
		{"stats error", failing, stats, get, "/v1/stats", "", 500, "failed to get stats: boom\n"},
		{"ingest bad JSON", ok, ingest, post, "/v1/papers/ingest", `{`, 400, "invalid JSON body\n"},
		{"ingest no input", ok, ingest, post, "/v1/papers/ingest", `{"arxiv_id":" ","url":""}`, 400, "arxiv_id or url is required\n"},
		{"ingest not configured", noIngester, ingest, post, "/v1/papers/ingest", `{"arxiv_id":"2401.00001"}`, 503, notConfigured},
		{"discover bad JSON", ok, discover, post, "/v1/papers/discover", `{`, 400, "invalid JSON body\n"},
		{"discover empty query", ok, discover, post, "/v1/papers/discover", `{"query":"  "}`, 400, "query is required\n"},
		{"discover not configured", noIngester, discover, post, "/v1/papers/discover", `{"query":"q"}`, 503, notConfigured},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logBuf := redirectServerLog(t)
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			if err := func() (err error) {
				defer func() {
					if r := recover(); r != nil {
						err = fmt.Errorf("handler panicked: %v", r)
					}
				}()
				tc.handler(tc.server())(rec, req)
				return nil
			}(); err != nil {
				t.Fatal(err)
			}

			if rec.Code != tc.wantCode {
				t.Errorf("code = %d, want %d", rec.Code, tc.wantCode)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
				t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", ct)
			}
			body := rec.Body.String()
			if !strings.HasPrefix(body, tc.wantBody) {
				t.Errorf("body = %q, want prefix %q", body, tc.wantBody)
			}
			if json.Valid([]byte(body)) {
				t.Errorf("body is JSON on an error path: %q", body)
			}
			if lines := nonEmptyLines(logBuf.String()); len(lines) != 0 {
				t.Errorf("unexpected log lines: %q", lines)
			}
		})
	}

	// q2: a real *PaperIngester must be stored as-is. Nothing is called on it.
	t.Run("SetIngester non-nil", func(t *testing.T) {
		ing := NewPaperIngester(nil, nil, "bucket", "", 0)
		s := NewServer(nil, nil, nil, 0)
		s.SetIngester(ing)
		if s.ingester == nil {
			t.Fatal("SetIngester(non-nil) left s.ingester nil")
		}
		if got, isPI := s.ingester.(*PaperIngester); !isPI || got != ing {
			t.Fatalf("s.ingester = %T %p, want the *PaperIngester passed in (%p)", s.ingester, s.ingester, ing)
		}
	})
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
