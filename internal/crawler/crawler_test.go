package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arxivlens/internal/arxiv"
	"arxivlens/internal/db"
	"arxivlens/shared/events"
)

func TestParseAtomFeed(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "arxiv_feed.xml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	papers, err := parseAtomFeed(body)
	if err != nil {
		t.Fatalf("parseAtomFeed: %v", err)
	}

	tests := []struct {
		arxivID    string
		version    int
		title      string
		abstract   string
		authors    []string
		categories []string
		published  time.Time
		sourceURL  string
	}{
		{
			arxivID:    "2307.08691",
			version:    2,
			title:      "FlashAttention-2: Faster Attention with Better Parallelism and Work Partitioning",
			abstract:   "Scaling Transformers to longer sequence lengths has been a major problem.",
			authors:    []string{"Tri Dao"},
			categories: []string{"cs.LG", "cs.AI"},
			published:  time.Date(2023, 7, 17, 17, 50, 36, 0, time.UTC),
			sourceURL:  "https://arxiv.org/pdf/2307.08691v2.pdf",
		},
		{
			arxivID:    "2401.12345",
			version:    1,
			title:      "A Paper Without Version",
			abstract:   "Short abstract.",
			authors:    []string{"Ada Lovelace", "Alan Turing"},
			categories: []string{"cs.CL"},
			published:  time.Date(2024, 1, 22, 9, 0, 0, 0, time.UTC),
			sourceURL:  "https://arxiv.org/pdf/2401.12345.pdf",
		},
	}

	if len(papers) != len(tests) {
		t.Fatalf("got %d papers, want %d", len(papers), len(tests))
	}

	for i, tt := range tests {
		t.Run(tt.arxivID, func(t *testing.T) {
			p := papers[i]
			if p.ArxivID != tt.arxivID {
				t.Errorf("ArxivID = %q, want %q", p.ArxivID, tt.arxivID)
			}
			if p.Version != tt.version {
				t.Errorf("Version = %d, want %d", p.Version, tt.version)
			}
			if p.Title != tt.title {
				t.Errorf("Title = %q, want %q", p.Title, tt.title)
			}
			if p.Abstract != tt.abstract {
				t.Errorf("Abstract = %q, want %q", p.Abstract, tt.abstract)
			}
			if !reflect.DeepEqual(p.Authors, tt.authors) {
				t.Errorf("Authors = %q, want %q", p.Authors, tt.authors)
			}
			if !reflect.DeepEqual(p.Categories, tt.categories) {
				t.Errorf("Categories = %q, want %q", p.Categories, tt.categories)
			}
			if !p.PublishedAt.Equal(tt.published) {
				t.Errorf("PublishedAt = %v, want %v", p.PublishedAt, tt.published)
			}
			if p.SourceURL != tt.sourceURL {
				t.Errorf("SourceURL = %q, want %q", p.SourceURL, tt.sourceURL)
			}
		})
	}
}

func TestParseAtomFeedEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantErr   bool
		wantCount int
	}{
		{name: "empty feed", body: `<feed xmlns="http://www.w3.org/2005/Atom"></feed>`, wantCount: 0},
		{name: "malformed xml", body: `<feed><entry>`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			papers, err := parseAtomFeed([]byte(tt.body))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if len(papers) != tt.wantCount {
				t.Errorf("got %d papers, want %d", len(papers), tt.wantCount)
			}
		})
	}
}

// CI fix: a 429 from arXiv (shared runner IPs) is retried with backoff instead of failing the crawl, and the
// crawler fetches from ARXIV_API_URL.
func TestCrawlRetriesRateLimit(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"></feed>`)) // no entries: crawl ends
	}))
	defer srv.Close()
	t.Setenv("ARXIV_API_URL", srv.URL)
	c := NewCrawler(nil, nil, time.Millisecond)
	c.lim = arxiv.NewLimiter(time.Millisecond)
	c.retry = arxiv.Retry{Attempts: 3, Base: time.Millisecond}
	n, err := c.Crawl(context.Background(), "cat:cs.LG", 2)
	if err != nil || n != 0 || hits.Load() != 2 {
		t.Fatalf("Crawl = %d, %v after %d requests; want 0, nil after one retry", n, err, hits.Load())
	}

	// Retries exhausted: the error names the status instead of a parse failure.
	always := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer always.Close()
	t.Setenv("ARXIV_API_URL", always.URL)
	c.retry = arxiv.NoRetry
	if _, err := c.Crawl(context.Background(), "cat:cs.LG", 2); err == nil || !strings.Contains(err.Error(), "status 429") {
		t.Errorf("err = %v, want a status 429 error", err)
	}
}

type nopUpserter struct{}

func (nopUpserter) UpsertDiscoveredPaper(context.Context, *db.PaperRecord) (int64, error) {
	return 1, nil
}

type countPub struct{ n int }

func (p *countPub) Publish(context.Context, string, events.EventEnvelope) error { p.n++; return nil }

// AC12 (ING-9b): NewCrawler never goes below arXiv's 3 s, and every Crawl request, a 429 retry included, waits on
// the limiter: 3 pages + 1 retry are 4 requests, each at least the interval after the one before.
func TestCrawlWaitsOnLimiter(t *testing.T) {
	for _, rl := range []time.Duration{0, time.Second, 5 * time.Second} {
		if got := NewCrawler(nil, nil, rl).lim.Interval(); got != max(rl, arxiv.DefaultInterval) {
			t.Errorf("NewCrawler(rate-limit %v): limiter interval %v, want %v", rl, got, max(rl, arxiv.DefaultInterval))
		}
	}
	const interval = 80 * time.Millisecond
	var mu sync.Mutex
	var at []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		at = append(at, time.Now())
		n := len(at)
		mu.Unlock()
		if n == 2 { // the second page is rate-limited once
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		start := r.URL.Query().Get("start")
		_, _ = fmt.Fprintf(w, `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>http://arxiv.org/abs/2401.0%s000v1</id>`+
			`<published>2024-01-01T00:00:00Z</published><title>t</title></entry></feed>`, start)
	}))
	defer srv.Close()
	t.Setenv("ARXIV_API_URL", srv.URL)
	pub := &countPub{}
	c := NewCrawler(nopUpserter{}, pub, 0)
	c.lim = arxiv.NewLimiter(interval)
	c.retry = arxiv.Retry{Attempts: 2, Base: time.Microsecond} // the backoff alone would allow an immediate retry
	n, err := c.Crawl(context.Background(), "cat:cs.LG", 41)   // pages start=0, 20, 40 (page size 20)
	if err != nil || n != 3 || pub.n != 3 {
		t.Fatalf("Crawl = %d, %v (published %d); want 3 papers over 3 pages", n, err, pub.n)
	}
	if len(at) != 4 {
		t.Fatalf("%d requests, want 4 (3 pages + 1 retry)", len(at))
	}
	for i := 1; i < len(at); i++ {
		if gap := at[i].Sub(at[i-1]); gap < interval-5*time.Millisecond {
			t.Errorf("request %d came %v after request %d, want >= %v (limiter)", i+1, gap, i, interval)
		}
	}
}
