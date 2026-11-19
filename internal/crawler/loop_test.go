package crawler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arxivlens/internal/arxiv"
	"arxivlens/internal/db"
)

var t0 = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

// at returns paper id published m minutes after t0 (m < 0: no parsable published time).
func at(id string, m int) db.PaperRecord {
	p := db.PaperRecord{ArxivID: id, Version: 1}
	if m >= 0 {
		p.PublishedAt = t0.Add(time.Duration(m) * time.Minute)
	}
	return p
}

// fakeFeed serves entries (newest-first) as pages and records each request as "start,n". cancel, if set, is
// called on every request.
type fakeFeed struct {
	entries []db.PaperRecord
	err     error
	calls   []string
	cancel  context.CancelFunc
}

func (f *fakeFeed) Page(_ context.Context, _ string, start, n int) ([]db.PaperRecord, error) {
	f.calls = append(f.calls, fmt.Sprintf("%d,%d", start, n))
	if f.cancel != nil {
		f.cancel()
	}
	lo, hi := min(start, len(f.entries)), min(start+n, len(f.entries))
	return f.entries[lo:hi], f.err
}

// feed builds entries from minutes; IDs are "p<minute>", a repeated minute repeats the paper, -1 is undated.
func feed(minutes ...int) []db.PaperRecord {
	var out []db.PaperRecord
	for i, m := range minutes {
		id := fmt.Sprintf("p%d", m)
		if m < 0 {
			id = fmt.Sprintf("undated%d", i)
		}
		out = append(out, at(id, m))
	}
	return out
}

func ids(ps []db.PaperRecord) string {
	var s []string
	for _, p := range ps {
		s = append(s, p.ArxivID)
	}
	return strings.Join(s, " ")
}

func TestWalkStopsAtWatermark(t *testing.T) {
	var big []int // 150 new entries, minutes 300..151
	for m := 300; m > 150; m-- {
		big = append(big, m)
	}
	bigWant := ids(feed(big[:103]...))
	w := t0.Add(10 * time.Minute)
	for _, c := range []struct {
		name       string
		entries    []db.PaperRecord
		hasW       bool
		limit, max int
		err        error
		calls      string
		want       string
		outOfOrder int
		wantErr    string
	}{
		{"5 new + 5 old, page 3", feed(15, 14, 13, 12, 11, 9, 8, 7, 6, 5), true, 3, 100, nil, "0,3 3,3", "p15 p14 p13 p12 p11", 0, ""},
		{"nothing new", feed(9, 8, 7, 6), true, 3, 100, nil, "0,3", "", 0, ""},
		{"entry == W is not a candidate", feed(12, 10, 9), true, 3, 100, nil, "0,3", "p12", 0, ""},
		{"old entry on top, page's last > W: go on", feed(9, 13, 12, 11, 8, 7), true, 3, 100, nil, "0,3 3,3", "p13 p12 p11", 1, ""},
		{"repeat across pages deduped", feed(15, 14, 13, 13, 12, 9), true, 3, 100, nil, "0,3 3,3", "p15 p14 p13 p12", 0, ""},
		{"undated ignored, not a boundary", feed(15, -1, 14, -1, -1, -1, 13, 9), true, 3, 100, nil, "0,3 3,3 6,3", "p15 p14 p13", 0, ""},
		{"page ending [<=W, undated] stops", feed(15, 9, -1, 8, 7, 6), true, 3, 100, nil, "0,3", "p15", 0, ""},
		{"out_of_order across an undated entry", feed(15, 13, -1, 14, 12, 9), true, 3, 100, nil, "0,3 3,3", "p15 p13 p14 p12", 1, ""},
		{"short page stops", feed(15, 14, 13, 12), true, 3, 100, nil, "0,3 3,3", "p15 p14 p13 p12", 0, ""},
		{"first run limit 4", feed(15, 14, 13, 12, 11, 9), false, 4, 100, nil, "0,4", "p15 p14 p13 p12", 0, ""},
		{"first run: undated not a candidate", feed(15, -1, 14), false, 3, 100, nil, "0,3", "p15 p14", 0, ""},
		{"first run limit 103: n = remainder", feed(big...), false, 103, 2000, nil, "0,100 100,3", bigWant, 0, ""},
		{"backlog > maxScan", feed(30, 29, 28, 27, 26, 25, 24, 23), true, 3, 6, nil, "0,3 3,3", "", 0, "exceeds --max-scan 6"},
		{"fetch error", feed(15), true, 3, 100, errors.New("boom"), "0,3", "", 0, "boom"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeFeed{entries: c.entries, err: c.err}
			got, st, err := walk(context.Background(), f, "cs.LG", w, c.hasW, c.limit, c.max)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) || got != nil {
					t.Fatalf("got (%q, %v), want error %q and no candidates", ids(got), err, c.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if calls := strings.Join(f.calls, " "); calls != c.calls {
				t.Errorf("requests %q, want %q", calls, c.calls)
			}
			if ids(got) != c.want {
				t.Errorf("candidates %q, want %q", ids(got), c.want)
			}
			if st.OutOfOrder != c.outOfOrder || (c.err == nil && st.Pages != len(f.calls)) {
				t.Errorf("stats %+v, want out_of_order=%d pages=%d", st, c.outOfOrder, len(f.calls))
			}
		})
	}
	t.Run("ctx cancelled between requests", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		f := &fakeFeed{entries: feed(15, 14, 13, 12, 11, 9), cancel: cancel}
		got, _, err := walk(ctx, f, "cs.LG", w, true, 3, 100)
		if !errors.Is(err, context.Canceled) || got != nil || strings.Join(f.calls, " ") != "0,3" {
			t.Errorf("got (%q, %v) after requests %q; want context.Canceled, no candidates, one request", ids(got), err, f.calls)
		}
	})
}

func TestSelectBatch(t *testing.T) {
	for _, c := range []struct {
		name  string
		in    []db.PaperRecord
		limit int
		want  string
	}{
		{"<= limit: all, ascending", feed(3, 1, 2), 5, "p1 p2 p3"},
		{"equal published: by arxiv_id", []db.PaperRecord{at("y", 1), at("x", 1), at("a", 2)}, 3, "x y a"},
		{"> limit: oldest limit", feed(5, 4, 3, 2, 1), 2, "p1 p2"},
		{"tie group straddling the cut is kept whole", []db.PaperRecord{at("e", 3), at("d", 2), at("c", 2), at("b", 2), at("a", 1)}, 2, "a b c d"},
	} {
		if got := ids(selectBatch(c.in, c.limit)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

type hit struct {
	query, ua string
	at        time.Time
}

// serveFeed points ARXIV_API_URL at a server answering codes[i] to request i (then 200), always with the
// recorded-style Atom body, so a non-200 that got parsed would show up as papers.
func serveFeed(t *testing.T, interval time.Duration, codes ...int) (*arxivFeed, func() []hit) {
	atom, err := os.ReadFile("testdata/arxiv_feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	hits := make(chan hit, 16)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- hit{r.URL.Path + "?" + r.URL.RawQuery, r.UserAgent(), time.Now()}
		if i := int(n.Add(1)) - 1; i < len(codes) {
			w.WriteHeader(codes[i])
		}
		_, _ = w.Write(atom)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ARXIV_API_URL", srv.URL+"/api/query")
	f := NewArxivFeed(srv.Client(), arxiv.NewLimiter(interval)).(*arxivFeed)
	f.retry = arxiv.Retry{Attempts: 2, Base: time.Millisecond}
	return f, func() (out []hit) {
		for len(hits) > 0 {
			out = append(out, <-hits)
		}
		return out
	}
}

func TestArxivFeedRequest(t *testing.T) {
	ctx := context.Background()
	// Byte-identical to what Crawl sends for `crawl --query "cat:cs.LG" --limit 2` (stack.yml's AC3 step log), the
	// query the CI arXiv replay store holds (its key is the same sorted urlencoding).
	const want = "/api/query?max_results=2&search_query=cat%3Acs.LG&sortBy=submittedDate&sortOrder=descending&start=0"
	f, hits := serveFeed(t, 0)
	got, _, err := walk(ctx, f, "cs.LG", time.Time{}, false, 2, 2000)
	if h := hits(); err != nil || len(got) != 2 || len(h) != 1 || h[0].query != want {
		t.Fatalf("first run limit 2: %d papers, err %v, requests %+v; want 2 papers and one request %q", len(got), err, h, want)
	}

	f, hits = serveFeed(t, 50*time.Millisecond)
	for _, q := range []string{"cat:cs.AI", "cat:cs.LG"} {
		if _, err := f.Page(ctx, q, 0, 2); err != nil {
			t.Fatal(err)
		}
	}
	h := hits()
	if len(h) != 2 || h[1].at.Sub(h[0].at) < 45*time.Millisecond {
		t.Fatalf("two categories: requests %+v, want 2 spaced by the shared 50ms limiter", h)
	}
	for _, x := range h {
		if x.ua != arxiv.UserAgent("Crawl Loop") {
			t.Errorf("User-Agent %q", x.ua)
		}
	}

	f, hits = serveFeed(t, 0, http.StatusTooManyRequests)
	if got, err := f.Page(ctx, "cat:cs.LG", 0, 2); err != nil || len(got) != 2 || len(hits()) != 2 {
		t.Errorf("429 then 200: %d papers, err %v; want 2 papers after 2 requests", len(got), err)
	}

	f, hits = serveFeed(t, 0, 503, 503)
	got, err = f.Page(ctx, "cat:cs.LG", 0, 2)
	var se *arxiv.StatusError
	if !errors.As(err, &se) || se.Code != 503 || got != nil || len(hits()) != 2 {
		t.Errorf("503 after retries: %d papers, err %v; want *arxiv.StatusError 503 after 2 requests, nothing parsed", len(got), err)
	}
}

func TestNewArxivFeedDefaults(t *testing.T) {
	lim := arxiv.NewLimiter(time.Second)
	f := NewArxivFeed(http.DefaultClient, lim).(*arxivFeed)
	r, d := f.retry, arxiv.DefaultRetry
	if f.client != http.DefaultClient || f.lim != lim || r.Attempts != d.Attempts || r.Base != d.Base || r.Max != d.Max {
		t.Errorf("NewArxivFeed: client %p lim %p retry %+v; want the given client and limiter, arxiv.DefaultRetry %+v", f.client, f.lim, r, d)
	}
	if f = NewArxivFeed(http.DefaultClient, nil).(*arxivFeed); f.lim == nil || f.lim.Interval() != arxiv.DefaultInterval {
		t.Errorf("NewArxivFeed(client, nil): limiter %+v, want a new arxiv.DefaultInterval limiter", f.lim)
	}
}

func TestParseCategories(t *testing.T) {
	for in, want := range map[string][]string{
		"cs.AI": {"cs.AI"}, "hep-th": {"hep-th"}, "math.CO": {"math.CO"},
		"cs.AI, cs.LG,cs.AI": {"cs.AI", "cs.LG"}, // trimmed, deduped, order kept
		"":                   nil,                // nil: rejected
		"   ":                nil, "cs.AI OR x": nil, "cat:cs.AI": nil, "cs.AI,": nil,
	} {
		got, err := ParseCategories(in)
		if !slices.Equal(got, want) || (want == nil) != (err != nil) {
			t.Errorf("ParseCategories(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
