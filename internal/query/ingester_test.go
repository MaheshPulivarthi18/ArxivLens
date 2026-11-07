package query

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"arxivlens/internal/arxiv"
	"arxivlens/internal/config"
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
			// arXiv metadata/PDF fetches keep their own shorter timeout.
			if got := ing.httpClient.Timeout; got != 45*time.Second {
				t.Fatalf("arXiv client timeout = %s, want 45s", got)
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
		stub, want := &arxivStub{}, []string{"id_list=2307.08691", "arxiv.org/pdf/2307.08691.pdf"}
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
