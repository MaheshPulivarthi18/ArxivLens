package query

import (
	"bytes"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
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
			var logBuf bytes.Buffer
			log.SetOutput(&logBuf)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })

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

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
