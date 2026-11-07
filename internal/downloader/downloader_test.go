package downloader

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"arxivlens/internal/arxiv"
)

type pdfStub struct {
	at     []time.Time
	uas    []string
	status int
}

func (s *pdfStub) RoundTrip(r *http.Request) (*http.Response, error) {
	s.at = append(s.at, time.Now())
	s.uas = append(s.uas, r.Header.Get("User-Agent"))
	return &http.Response{StatusCode: s.status, Body: io.NopCloser(strings.NewReader("%PDF-1.4")), Request: r}, nil
}

func TestFetchPDFIsSpacedAndIdentified(t *testing.T) {
	d := NewDownloader(nil, nil, nil)
	if d.limiter == nil || d.limiter.Interval() != 3*time.Second {
		t.Fatalf("NewDownloader limiter not set to the 3s arxiv.DefaultInterval")
	}
	const interval, slack = 50 * time.Millisecond, 10 * time.Millisecond
	stub := &pdfStub{status: http.StatusOK}
	d.client = &http.Client{Transport: stub, Timeout: 5 * time.Second}
	d.limiter = arxiv.NewLimiter(interval)
	for i := 0; i < 2; i++ {
		if body, err := d.fetchPDF(context.Background(), "https://arxiv.org/pdf/2307.08691.pdf"); err != nil || string(body) != "%PDF-1.4" {
			t.Fatalf("fetchPDF %d = %q, %v", i, body, err)
		}
	}
	if len(stub.at) != 2 || stub.at[1].Sub(stub.at[0]) < interval-slack {
		t.Fatalf("PDF requests at %v, want 2 that are >= %s apart", stub.at, interval)
	}
	for i, ua := range stub.uas {
		if ua != "ArxivLens/1.0 (Downloader Pipeline; mailto:contact@arxivlens.local)" {
			t.Errorf("request %d User-Agent = %q", i, ua)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.fetchPDF(ctx, "https://arxiv.org/pdf/x.pdf"); err == nil || len(stub.at) != 2 {
		t.Fatalf("fetchPDF(cancelled) = %v after %d requests, want an error and no request", err, len(stub.at))
	}
	stub.status = http.StatusServiceUnavailable
	if _, err := d.fetchPDF(context.Background(), "https://arxiv.org/pdf/x.pdf"); err == nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("fetchPDF on 503 = %v, want a status error", err)
	}
}
