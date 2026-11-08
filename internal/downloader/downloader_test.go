package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"arxivlens/internal/arxiv"
	"arxivlens/shared/events"
	"github.com/twmb/franz-go/pkg/kgo"
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

// downloadFakes records what HandleRecord does: the PDF URLs it requests (pdfStub-like transport),
// the storage keys it puts, the DB rows it updates and the envelopes it publishes.
type downloadFakes struct {
	urls, puts, dbKeys []string
	dbVersions         []int
	published          []events.EventEnvelope
}

func (f *downloadFakes) RoundTrip(r *http.Request) (*http.Response, error) {
	f.urls = append(f.urls, r.URL.String())
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("%PDF-1.4")), Request: r}, nil
}
func (f *downloadFakes) ObjectExists(context.Context, string) (bool, error) { return false, nil }
func (f *downloadFakes) DownloadPDF(context.Context, string) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (f *downloadFakes) UploadPDF(_ context.Context, key string, _ io.Reader, _ int64) error {
	f.puts = append(f.puts, key)
	return nil
}
func (f *downloadFakes) UpdatePaperDownloaded(_ context.Context, _ string, version int, key, _ string) error {
	f.dbVersions, f.dbKeys = append(f.dbVersions, version), append(f.dbKeys, key)
	return nil
}
func (f *downloadFakes) Publish(_ context.Context, _ string, env events.EventEnvelope) error {
	f.published = append(f.published, env)
	return nil
}

// HandleRecord fetches exactly one PDF, the one of the envelope's version, and labels everything
// with that version. A version-less envelope keeps the v1 default (it logs a warning).
func TestHandleRecordFetchesTheEnvelopesVersion(t *testing.T) {
	tests := []struct{ envVersion, want int }{{1, 1}, {2, 2}, {0, 1}}
	for _, tt := range tests {
		f := &downloadFakes{}
		d := &Downloader{database: f, storage: f, bucket: "b", producer: f,
			client: &http.Client{Transport: f}, limiter: arxiv.NewLimiter(time.Millisecond)}
		env := events.NewEventEnvelope(events.TopicPapersDiscovered, "2307.08691", tt.envVersion, nil)
		value, _ := json.Marshal(env)
		if err := d.HandleRecord(context.Background(), &kgo.Record{Value: value}); err != nil {
			t.Fatalf("v%d: HandleRecord: %v", tt.envVersion, err)
		}
		wantURL := fmt.Sprintf("https://arxiv.org/pdf/2307.08691v%d.pdf", tt.want)
		wantKey := fmt.Sprintf("raw/2307.08691_v%d.pdf", tt.want)
		if len(f.urls) != 1 || f.urls[0] != wantURL {
			t.Errorf("v%d: PDF requests = %q, want exactly [%s]", tt.envVersion, f.urls, wantURL)
		}
		if len(f.puts) != 1 || f.puts[0] != wantKey || len(f.dbKeys) != 1 || f.dbKeys[0] != wantKey || f.dbVersions[0] != tt.want {
			t.Errorf("v%d: puts %q, db keys %q versions %v, want %s and v%d", tt.envVersion, f.puts, f.dbKeys, f.dbVersions, wantKey, tt.want)
		}
		if len(f.published) != 1 || f.published[0].Version != tt.want || f.published[0].Payload["version"] != tt.want {
			t.Errorf("v%d: published %+v, want one papers.downloaded envelope at v%d", tt.envVersion, f.published, tt.want)
		}
	}
}
