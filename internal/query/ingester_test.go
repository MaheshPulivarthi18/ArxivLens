package query

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"arxivlens/internal/arxiv"
	"arxivlens/internal/config"
	"arxivlens/internal/db"
	"github.com/minio/minio-go/v7"
)

// Pins current behaviour of ExtractCleanArxivID (regex \d{4}\.\d{4,5}).
// Old-style IDs such as cs/0601001 are not supported today.
func TestExtractCleanArxivID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "bare 5-digit suffix", input: "2307.08691", want: "2307.08691"},
		{name: "bare 4-digit suffix", input: "1706.0376", want: "1706.0376"},
		{name: "versioned v2", input: "2307.08691v2", want: "2307.08691"},
		{name: "versioned v12", input: "2401.12345v12", want: "2401.12345"},
		{name: "abs URL", input: "https://arxiv.org/abs/2307.08691", want: "2307.08691"},
		{name: "abs URL versioned", input: "http://arxiv.org/abs/2307.08691v3", want: "2307.08691"},
		{name: "pdf URL", input: "https://arxiv.org/pdf/2307.08691", want: "2307.08691"},
		{name: "pdf URL with .pdf suffix", input: "https://arxiv.org/pdf/2307.08691v2.pdf", want: "2307.08691"},
		{name: ".pdf suffix", input: "2307.08691.pdf", want: "2307.08691"},
		{name: "surrounding whitespace", input: "  \t2307.08691\n ", want: "2307.08691"},
		{name: "empty string", input: "", wantErr: true},
		{name: "whitespace only", input: "   ", wantErr: true},
		{name: "free text", input: "attention is all you need", wantErr: true},
		{name: "old-style ID unsupported", input: "cs/0601001", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractCleanArxivID(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ExtractCleanArxivID(%q) = %q, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExtractCleanArxivID(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ExtractCleanArxivID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNewPaperIngesterTimeouts(t *testing.T) {
	tests := []struct {
		name          string
		ingestTimeout time.Duration
		wantIngest    time.Duration
	}{
		{name: "explicit", ingestTimeout: 7 * time.Minute, wantIngest: 7 * time.Minute},
		{name: "zero uses default", ingestTimeout: 0, wantIngest: config.DefaultIngestTimeout},
		{name: "negative uses default", ingestTimeout: -time.Second, wantIngest: config.DefaultIngestTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ing := NewPaperIngester(nil, nil, "bucket", "", tt.ingestTimeout)
			if ing.ingestClient == nil || ing.httpClient == nil {
				t.Fatalf("clients not initialised")
			}
			if got := ing.ingestClient.Timeout; got != tt.wantIngest {
				t.Fatalf("ingest timeout = %s, want %s", got, tt.wantIngest)
			}
			// arXiv metadata fetches keep their own shorter timeout; PDFs use stall-based limits instead.
			if got := ing.httpClient.Timeout; got != 45*time.Second {
				t.Fatalf("arXiv client timeout = %s, want 45s", got)
			}
			if ing.pdfLimits != arxiv.DefaultPDFLimits {
				t.Fatalf("pdfLimits = %+v, want arxiv.DefaultPDFLimits", ing.pdfLimits)
			}
			if ing.limiter == nil || ing.limiter.Interval() != arxiv.DefaultInterval {
				t.Fatalf("arXiv limiter not set to DefaultInterval")
			}
		})
	}
}

// arxivStub records every arXiv request and answers without network: Atom queries get a
// one-entry feed, PDFs get 503 so IngestPaper stops before MinIO and the DB.
type arxivStub struct {
	mu   sync.Mutex
	reqs []recordedReq
}

type recordedReq struct {
	at      time.Time
	url, ua string
}

const stubFeed = `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>http://arxiv.org/abs/2307.08691v1</id>` +
	`<title>Stub Paper</title></entry></feed>`

func (s *arxivStub) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, recordedReq{time.Now(), r.URL.String(), r.Header.Get("User-Agent")})
	s.mu.Unlock()
	status, body := http.StatusOK, stubFeed
	if strings.HasSuffix(r.URL.Path, ".pdf") {
		status, body = http.StatusServiceUnavailable, ""
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

const testInterval, testSlack = 50 * time.Millisecond, 10 * time.Millisecond

func newStubIngester(stub *arxivStub) *PaperIngester {
	ing := NewPaperIngester(nil, nil, "bucket", "", 0)
	ing.httpClient = &http.Client{Transport: stub, Timeout: 5 * time.Second}
	ing.limiter = arxiv.NewLimiter(testInterval)
	return ing
}

// checkSpaced: requests match wantURLs in start order, start >= testInterval apart (testSlack covers
// permit-to-transport jitter) and carry the ingester's User-Agent.
func checkSpaced(t *testing.T, stub *arxivStub, wantURLs ...string) {
	t.Helper()
	reqs := stub.reqs
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].at.Before(reqs[j].at) })
	if len(reqs) != len(wantURLs) {
		t.Fatalf("got %d arXiv requests, want %d: %+v", len(reqs), len(wantURLs), reqs)
	}
	for i, r := range reqs {
		if !strings.Contains(r.url, wantURLs[i]) || r.ua != "ArxivLens/1.0 (Query Ingester; mailto:contact@arxivlens.local)" {
			t.Errorf("request %d = %s with User-Agent %q, want %q and the Query Ingester UA", i, r.url, r.ua, wantURLs[i])
		}
		if i > 0 && r.at.Sub(reqs[i-1].at) < testInterval-testSlack {
			t.Errorf("request %d started %s after the previous one, want >= %s", i, r.at.Sub(reqs[i-1].at), testInterval)
		}
	}
}

// IngestPaper sends Atom then PDF; DiscoverAndIngest sends search, Atom, PDF. All spaced and identified.
func TestIngestSpacesArxivRequests(t *testing.T) {
	for _, discover := range []bool{false, true} {
		stub, want := &arxivStub{}, []string{"id_list=2307.08691", "arxiv.org/pdf/2307.08691v1.pdf"}
		ing, err := newStubIngester(stub), error(nil)
		if discover {
			_, err = ing.DiscoverAndIngest(context.Background(), "flash attention")
			want = append([]string{"search_query=all:flash+attention"}, want...)
		} else {
			_, err = ing.IngestPaper(context.Background(), "2307.08691")
		}
		if err == nil || !strings.Contains(err.Error(), "status 503") {
			t.Fatalf("discover=%v: err = %v, want the stub's PDF 503", discover, err)
		}
		checkSpaced(t, stub, want...)
	}
}

func TestConcurrentIngestsShareOneLimiter(t *testing.T) {
	stub := &arxivStub{}
	ing := newStubIngester(stub)
	var wg sync.WaitGroup
	for _, id := range []string{"2307.08691", "2401.12345"} {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = ing.IngestPaper(context.Background(), id) }()
	}
	wg.Wait()
	checkSpaced(t, stub, "arxiv", "arxiv", "arxiv", "arxiv")
}

// ingestFakes stands in for arXiv (transport), Postgres and MinIO, and counts what IngestPaper did.
type ingestFakes struct {
	entryID   string
	arxivURLs []string
	records   []db.PaperRecord
	updates   []string // "<storage key>@v<version>"
	puts      []string
}

func (f *ingestFakes) RoundTrip(r *http.Request) (*http.Response, error) {
	f.arxivURLs = append(f.arxivURLs, r.URL.String())
	body := fmt.Sprintf(`<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>%s</id><title>T</title></entry></feed>`, f.entryID)
	if strings.HasSuffix(r.URL.Path, ".pdf") {
		body = "%PDF-1.4"
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}
func (f *ingestFakes) UpsertDiscoveredPaper(_ context.Context, p *db.PaperRecord) (int64, error) {
	f.records = append(f.records, *p)
	return 1, nil
}
func (f *ingestFakes) UpdatePaperDownloaded(_ context.Context, _ string, version int, key, _ string) error {
	f.updates = append(f.updates, fmt.Sprintf("%s@v%d", key, version))
	return nil
}
func (f *ingestFakes) PutObject(_ context.Context, _, object string, _ io.Reader, _ int64, _ minio.PutObjectOptions) (minio.UploadInfo, error) {
	f.puts = append(f.puts, object)
	return minio.UploadInfo{}, nil
}

// newFakeIngester wires f and a fake embedder whose /internal/ingest payloads land in *payloads.
func newFakeIngester(t *testing.T, f *ingestFakes, payloads *[]map[string]any) *PaperIngester {
	t.Helper()
	emb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		*payloads = append(*payloads, p)
		_, _ = io.WriteString(w, `{"status":"ok","sections":1,"chunks":2,"embeddings":2}`)
	}))
	t.Cleanup(emb.Close)
	ing := NewPaperIngester(nil, nil, "bucket", emb.URL, 0)
	ing.database, ing.minioClient = f, f
	ing.httpClient = &http.Client{Transport: f, Timeout: 5 * time.Second}
	ing.limiter = arxiv.NewLimiter(time.Millisecond)
	return ing
}

// AC1: the version in the Atom entry ID labels the PDF fetched, the stored row and the ingest call.
func TestIngestUsesEntryVersion(t *testing.T) {
	f, payloads := &ingestFakes{entryID: "http://arxiv.org/abs/2401.12345v3"}, []map[string]any{}
	res, err := newFakeIngester(t, f, &payloads).IngestPaper(context.Background(), "2401.12345")
	if err != nil {
		t.Fatalf("IngestPaper: %v", err)
	}
	const key, pdf = "raw/2401.12345_v3.pdf", "https://arxiv.org/pdf/2401.12345v3.pdf"
	if len(f.arxivURLs) != 2 || f.arxivURLs[1] != pdf {
		t.Errorf("arXiv requests = %q, want the Atom query then %s", f.arxivURLs, pdf)
	}
	if len(f.records) != 1 || f.records[0].Version != 3 || f.records[0].SourceURL != pdf || f.records[0].PDFStorageKey != key {
		t.Errorf("DB records = %+v, want one at v3, %s, %s", f.records, pdf, key)
	}
	if len(f.updates) != 1 || f.updates[0] != key+"@v3" || len(f.puts) != 1 || f.puts[0] != key {
		t.Errorf("DB updates %q, MinIO puts %q, want one each for %s at v3", f.updates, f.puts, key)
	}
	if len(payloads) != 1 || payloads[0]["version"] != float64(3) || payloads[0]["pdf_storage_key"] != key {
		t.Errorf("/internal/ingest payloads = %v, want one with version 3 and %s", payloads, key)
	}
	if res.Version != 3 {
		t.Errorf("result version = %d, want 3", res.Version)
	}
}

// AC2: no parseable version fails the ingest, naming the entry ID, before the PDF, MinIO or DB.
func TestIngestRejectsUnversionedEntry(t *testing.T) {
	for _, id := range []string{"http://arxiv.org/abs/2401.12345", "http://arxiv.org/abs/2401.12345v", "http://arxiv.org/abs/2401.12345v0", "http://arxiv.org/abs/2401.12345vx"} {
		f, payloads := &ingestFakes{entryID: id}, []map[string]any{}
		_, err := newFakeIngester(t, f, &payloads).IngestPaper(context.Background(), "2401.12345")
		if err == nil || !strings.Contains(err.Error(), id) {
			t.Errorf("%s: err = %v, want an error naming the entry ID", id, err)
		}
		if len(f.arxivURLs) != 1 || len(f.records)+len(f.updates)+len(f.puts)+len(payloads) != 0 {
			t.Errorf("%s: arXiv %q, DB %d+%d, MinIO %q, ingest %v; want only the Atom query", id, f.arxivURLs, len(f.records), len(f.updates), f.puts, payloads)
		}
	}
}
