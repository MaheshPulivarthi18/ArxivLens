package arxiv

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEndpoints(t *testing.T) {
	t.Setenv("ARXIV_API_URL", "")
	t.Setenv("ARXIV_PDF_BASE", "")
	if APIURL() != DefaultAPIURL || FetchURL("https://arxiv.org/pdf/2401.00001v2.pdf") != "https://arxiv.org/pdf/2401.00001v2.pdf" {
		t.Fatalf("defaults: %q %q", APIURL(), FetchURL("https://arxiv.org/pdf/2401.00001v2.pdf"))
	}
	t.Setenv("ARXIV_API_URL", " http://127.0.0.1:8099/api/query ")
	t.Setenv("ARXIV_PDF_BASE", "http://127.0.0.1:8099/pdf/")
	if got := APIURL(); got != "http://127.0.0.1:8099/api/query" {
		t.Errorf("APIURL = %q", got)
	}
	for in, want := range map[string]string{
		"https://arxiv.org/pdf/2401.00001v2.pdf": "http://127.0.0.1:8099/pdf/2401.00001v2.pdf",
		"https://example.com/x.pdf":              "https://example.com/x.pdf", // not arXiv: untouched
	} {
		if got := FetchURL(in); got != want {
			t.Errorf("FetchURL(%q) = %q, want %q", in, got, want)
		}
	}
	// The canonical URL (stored, shown to users) never changes.
	if PDFURL("2401.00001", 2) != "https://arxiv.org/pdf/2401.00001v2.pdf" {
		t.Error("PDFURL must stay canonical when ARXIV_PDF_BASE is set")
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestRetryable(t *testing.T) {
	for err, want := range map[error]bool{
		&StatusError{Code: 429}:     true,
		&StatusError{Code: 503}:     true,
		&StatusError{Code: 500}:     true,
		&StatusError{Code: 404}:     false,
		&StatusError{Code: 400}:     false,
		ErrPDFStalled:               true,
		timeoutErr{}:                true,
		errors.New("parse failure"): false,
		context.Canceled:            false,
	} {
		if got := Retryable(err); got != want {
			t.Errorf("Retryable(%v) = %v, want %v", err, got, want)
		}
	}
}

// recordWaits returns a Retry whose waits are recorded instead of slept.
func recordWaits(r Retry, waits *[]time.Duration) Retry {
	r.wait = func(_ context.Context, d time.Duration) error { *waits = append(*waits, d); return nil }
	return r
}

func TestRetryDo(t *testing.T) {
	ctx := context.Background()
	t.Run("backoff 5s 15s 45s then gives up with the last error", func(t *testing.T) {
		var waits []time.Duration
		calls := 0
		err := recordWaits(DefaultRetry, &waits).Do(ctx, func() error { calls++; return &StatusError{Code: 429} })
		if calls != 4 || !strings.Contains(err.Error(), "status 429") {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
		if want := []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second}; !equalDurations(waits, want) {
			t.Errorf("waits = %v, want %v", waits, want)
		}
	})
	t.Run("Retry-After raises the wait, Max caps it", func(t *testing.T) {
		var waits []time.Duration
		errs := []error{&StatusError{Code: 503, RetryAfter: 30 * time.Second}, &StatusError{Code: 429, RetryAfter: 10 * time.Minute}, nil}
		i := 0
		if err := recordWaits(DefaultRetry, &waits).Do(ctx, func() error { i++; return errs[i-1] }); err != nil {
			t.Fatal(err)
		}
		if want := []time.Duration{30 * time.Second, 60 * time.Second}; !equalDurations(waits, want) {
			t.Errorf("waits = %v, want %v", waits, want)
		}
	})
	t.Run("non-retryable error returns at once", func(t *testing.T) {
		var waits []time.Duration
		calls := 0
		err := recordWaits(DefaultRetry, &waits).Do(ctx, func() error { calls++; return &StatusError{Code: 404} })
		if calls != 1 || len(waits) != 0 || err == nil {
			t.Errorf("calls=%d waits=%v err=%v", calls, waits, err)
		}
	})
	t.Run("zero value and NoRetry make one attempt", func(t *testing.T) {
		for _, r := range []Retry{{}, NoRetry} {
			calls := 0
			_ = r.Do(ctx, func() error { calls++; return &StatusError{Code: 429} })
			if calls != 1 {
				t.Errorf("%+v: calls = %d, want 1", r, calls)
			}
		}
	})
	t.Run("cancelled context stops the wait", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		calls := 0
		r := Retry{Attempts: 4, Base: time.Hour}
		time.AfterFunc(20*time.Millisecond, cancel)
		start := time.Now()
		err := r.Do(cctx, func() error { calls++; return &StatusError{Code: 429} })
		if calls != 1 || time.Since(start) > 2*time.Second || !strings.Contains(err.Error(), "status 429") {
			t.Errorf("calls=%d after %s, err=%v", calls, time.Since(start), err)
		}
	})
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestGetAPI(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "ua-x" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		switch n := hits.Add(1); {
		case r.URL.Query().Get("q") == "missing":
			w.WriteHeader(http.StatusNotFound)
		case n == 1:
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests) // empty body: must not be parsed as a feed
		default:
			_, _ = w.Write([]byte("<feed/>"))
		}
	}))
	defer srv.Close()
	var waits []time.Duration
	r := recordWaits(Retry{Attempts: 3, Base: time.Millisecond}, &waits)
	lim := NewLimiter(time.Millisecond)
	body, err := GetAPI(context.Background(), srv.Client(), lim, srv.URL+"?q=ok", "ua-x", r)
	if err != nil || string(body) != "<feed/>" || hits.Load() != 2 {
		t.Fatalf("body=%q err=%v hits=%d, want the feed after one 429 retry", body, err, hits.Load())
	}
	if len(waits) != 1 || waits[0] != time.Second {
		t.Errorf("waits = %v, want [1s] from Retry-After", waits)
	}
	_, err = GetAPI(context.Background(), srv.Client(), nil, srv.URL+"?q=missing", "ua-x", r)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 404 || hits.Load() != 3 {
		t.Errorf("404: err=%v hits=%d, want one request and a StatusError", err, hits.Load())
	}
}
