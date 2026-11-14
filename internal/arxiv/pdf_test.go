package arxiv

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var testLimits = PDFLimits{Stall: 150 * time.Millisecond, Total: 5 * time.Second, MaxBytes: 1 << 20}

// A body that trickles in for longer than any old-style client timeout succeeds while bytes keep coming.
func TestFetchPDFSlowButSteadySucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != "ua-test" {
			t.Errorf("User-Agent = %q", ua)
		}
		for i := 0; i < 10; i++ { // 10 × 60 ms = 600 ms total, 4× the stall timeout
			_, _ = w.Write([]byte("chunk"))
			w.(http.Flusher).Flush()
			time.Sleep(60 * time.Millisecond)
		}
	}))
	defer srv.Close()
	client := &http.Client{Timeout: 100 * time.Millisecond} // must be ignored
	body, err := FetchPDF(context.Background(), client, srv.URL, "ua-test", testLimits)
	if err != nil {
		t.Fatalf("FetchPDF: %v", err)
	}
	if got := string(body); got != strings.Repeat("chunk", 10) {
		t.Fatalf("body = %q", got)
	}
}

func TestFetchPDFStallAborts(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("start"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	_, err := FetchPDF(context.Background(), http.DefaultClient, srv.URL, "ua", testLimits)
	if !errors.Is(err, ErrPDFStalled) {
		t.Fatalf("err = %v, want ErrPDFStalled", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("stall detected after %s", d)
	}
}

func TestFetchPDFStatusAndSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(make([]byte, 2048))
	}))
	defer srv.Close()
	if _, err := FetchPDF(context.Background(), http.DefaultClient, srv.URL+"/missing", "ua", testLimits); err == nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("503: err = %v", err)
	}
	small := testLimits
	small.MaxBytes = 1024
	if _, err := FetchPDF(context.Background(), http.DefaultClient, srv.URL, "ua", small); err == nil || !strings.Contains(err.Error(), "exceeds 1024 bytes") {
		t.Fatalf("oversize: err = %v", err)
	}
	if b, err := FetchPDF(context.Background(), http.DefaultClient, srv.URL, "ua", testLimits); err != nil || len(b) != 2048 {
		t.Fatalf("ok: len=%d err=%v", len(b), err)
	}
}

func TestFetchPDFParentCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	_, err := FetchPDF(ctx, http.DefaultClient, srv.URL, "ua", testLimits)
	if err == nil || errors.Is(err, ErrPDFStalled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled (not a stall)", err)
	}
}
