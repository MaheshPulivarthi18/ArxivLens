package crawler

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arxivlens/internal/arxiv"
	"arxivlens/internal/db"
	"arxivlens/shared/events"
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

// feeds serves one fakeFeed per category (query "cat:<category>").
type feeds map[string]*fakeFeed

func (fs feeds) Page(ctx context.Context, query string, start, n int) ([]db.PaperRecord, error) {
	return fs[strings.TrimPrefix(query, "cat:")].Page(ctx, query, start, n)
}

// fakeStore is the watermark table: GREATEST on write, and like pgx it fails on a cancelled ctx. It also
// requires a deadline (the 10 s write timeout).
type fakeStore struct {
	w        map[string]time.Time
	readErr  map[string]error
	writeErr error
	writes   []string
}

func (s *fakeStore) Watermark(_ context.Context, c string) (time.Time, bool, error) {
	t, ok := s.w[c]
	return t, ok, s.readErr[c]
}

func (s *fakeStore) AdvanceWatermark(ctx context.Context, c string, t time.Time) error {
	if _, ok := ctx.Deadline(); ctx.Err() != nil || !ok || s.writeErr != nil {
		return fmt.Errorf("watermark write: ctx %v, deadline %v, %v", ctx.Err(), ok, s.writeErr)
	}
	s.writes = append(s.writes, c)
	if t.After(s.w[c]) {
		s.w[c] = t
	}
	return nil
}

type paperKey struct {
	id string
	v  int
}

// fakeSink is the papers table (natural key) and the broker. failUpsert/failPublish fail that 1-based call;
// afterAck runs after every acked publish.
type fakeSink struct {
	papers                  map[paperKey]db.PaperRecord
	upserts, publishes      int
	acked                   []string // arXiv IDs in ack order
	failUpsert, failPublish int
	afterAck                func(acked int)
}

func (k *fakeSink) UpsertDiscoveredPaper(_ context.Context, p *db.PaperRecord) (int64, error) {
	if k.upserts++; k.upserts == k.failUpsert {
		return 0, errors.New("postgres down")
	}
	k.papers[paperKey{p.ArxivID, p.Version}] = *p
	return 1, nil
}

func (k *fakeSink) Publish(_ context.Context, _ string, e events.EventEnvelope) error {
	if k.publishes++; k.publishes == k.failPublish {
		return errors.New("broker down")
	}
	if k.acked = append(k.acked, e.PaperID); k.afterAck != nil {
		k.afterAck(len(k.acked))
	}
	return nil
}

// fixture builds a loop over fs (categories sorted) with watermarks w in minutes after t0 (absent: first run).
func fixture(fs feeds, limit int, w map[string]int) (*Loop, *fakeStore, *fakeSink) {
	st := &fakeStore{w: map[string]time.Time{}, readErr: map[string]error{}}
	for c, m := range w {
		st.w[c] = t0.Add(time.Duration(m) * time.Minute)
	}
	sink := &fakeSink{papers: map[paperKey]db.PaperRecord{}}
	cfg := LoopConfig{Categories: slices.Sorted(maps.Keys(fs)), Interval: time.Hour, LimitPerRun: limit, MaxScan: 1000}
	return NewLoop(cfg, fs, sink, sink, st), st, sink
}

func minute(t time.Time) int { return int(t.Sub(t0) / time.Minute) }

func TestWatermarkAfter(t *testing.T) {
	batch := []db.PaperRecord{at("a", 11), at("b", 12), at("c", 12), at("d", 13)}
	for k, want := range []int{-1, 11, 11, 12, 13} { // k papers emitted; -1: no watermark earned
		if got, ok := watermarkAfter(batch, k); ok != (want >= 0) || (ok && minute(got) != want) {
			t.Errorf("k=%d: got (%v, %v), want minute %d", k, got, ok, want)
		}
	}
	if _, ok := watermarkAfter(batch[1:3], 1); ok {
		t.Error("first group incomplete: want no watermark")
	}
}

func TestRunCategoryAdvancesWatermark(t *testing.T) {
	tie := []db.PaperRecord{at("e", 14), at("d", 13), at("c", 12), at("b", 12), at("a", 11), at("old", 9)}
	for _, c := range []struct {
		name    string
		entries []db.PaperRecord
		w       map[string]int
		limit   int
		passes  []string // acked per pass
		wm      []int    // watermark minute after each pass
		cands   string   // sorted
	}{
		{"untruncated: W = max seen", feed(15, 14, 13, 12, 11, 9), map[string]int{"cs.LG": 10}, 10,
			[]string{"p11 p12 p13 p14 p15"}, []int{15}, "p11 p12 p13 p14 p15"},
		{"truncated: oldest limit, then the rest", feed(15, 14, 13, 12, 11, 9), map[string]int{"cs.LG": 10}, 3,
			[]string{"p11 p12 p13", "p14 p15", ""}, []int{13, 15, 15}, "p11 p12 p13 p14 p15"},
		{"first run: newest limit, ascending", feed(15, 14, 13, 12, 9), nil, 3, []string{"p13 p14 p15", ""}, []int{15, 15}, "p13 p14 p15"},
		{"tie group straddling limit emitted whole", tie, map[string]int{"cs.LG": 10}, 2, []string{"a b c", "d e"}, []int{12, 14}, "a b c d e"},
	} {
		t.Run(c.name, func(t *testing.T) {
			l, st, sink := fixture(feeds{"cs.LG": {entries: c.entries}}, c.limit, c.w)
			for i, want := range c.passes {
				before := len(sink.acked)
				err := l.RunOnce(context.Background())
				if got := strings.Join(sink.acked[before:], " "); err != nil || got != want || minute(st.w["cs.LG"]) != c.wm[i] {
					t.Errorf("pass %d: err %v, acked %q, W=%d; want %q, W=%d", i+1, err, got, minute(st.w["cs.LG"]), want, c.wm[i])
				}
			}
			if union := slices.Sorted(slices.Values(sink.acked)); strings.Join(union, " ") != c.cands {
				t.Errorf("union over passes %q, want the candidates %q", union, c.cands)
			}
		})
	}
}

func TestRunOnceTwiceEmitsNothingSecondTime(t *testing.T) {
	l, st, sink := fixture(feeds{"cs.AI": {entries: feed(14, 12, 9)}, "cs.LG": {entries: feed(15, 13, 8)}}, 10, map[string]int{"cs.AI": 10})
	if err := l.RunOnce(context.Background()); err != nil || sink.publishes != 5 {
		t.Fatalf("pass 1: err %v, %d publishes; want 5", err, sink.publishes)
	}
	w, ups, pubs, writes := maps.Clone(st.w), sink.upserts, sink.publishes, len(st.writes)
	err := l.RunOnce(context.Background())
	if err != nil || sink.upserts != ups || sink.publishes != pubs || len(st.writes) != writes || !maps.Equal(st.w, w) {
		t.Errorf("pass 2: err %v, +%d upserts +%d publishes +%d watermark writes, W %v -> %v; want nothing new",
			err, sink.upserts-ups, sink.publishes-pubs, len(st.writes)-writes, w, st.w)
	}
}

func TestRunCategoryCrashAtK(t *testing.T) {
	entries := []db.PaperRecord{at("d", 13), at("c", 12), at("b", 12), at("a", 11), at("old", 9)} // b, c: one group
	asc := []db.PaperRecord{entries[3], entries[2], entries[1], entries[0]}
	type row struct {
		name                    string
		failUpsert, failPublish int
		dupOK                   func(db.PaperRecord) bool // may be published in both passes
	}
	var rows []row
	for k, failed := range asc {
		group := func(p db.PaperRecord) bool { return p.PublishedAt.Equal(failed.PublishedAt) }
		rows = append(rows, row{fmt.Sprintf("upsert k=%d", k), k + 1, 0, group}, row{fmt.Sprintf("publish k=%d", k), 0, k + 1, group})
	}
	rows = append(rows, row{"k=n: watermark write fails", 0, 0, nil})
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			l, st, sink := fixture(feeds{"cs.LG": {entries: entries}}, 10, map[string]int{"cs.LG": 10})
			sink.failUpsert, sink.failPublish = r.failUpsert, r.failPublish
			if r.dupOK == nil {
				st.writeErr, r.dupOK = errors.New("killed before the write"), func(db.PaperRecord) bool { return true }
			}
			if err := l.RunOnce(context.Background()); err == nil {
				t.Fatal("pass 1: want an error")
			}
			pass1 := slices.Clone(sink.acked)
			for _, p := range asc {
				if !slices.Contains(pass1, p.ArxivID) && !p.PublishedAt.After(st.w["cs.LG"]) {
					t.Errorf("W=%d is at or above un-acked %s (%d)", minute(st.w["cs.LG"]), p.ArxivID, minute(p.PublishedAt))
				}
			}
			sink.failUpsert, sink.failPublish, st.writeErr = 0, 0, nil
			if err := l.RunOnce(context.Background()); err != nil {
				t.Fatalf("pass 2: %v", err)
			}
			for _, p := range asc {
				in1, in2 := slices.Contains(pass1, p.ArxivID), slices.Contains(sink.acked[len(pass1):], p.ArxivID)
				if _, row := sink.papers[paperKey{p.ArxivID, 1}]; !row || (!in1 && !in2) || (in1 && in2 && !r.dupOK(p)) {
					t.Errorf("%s: row %v, pass 1 %v, pass 2 %v; want a row, published, twice only if in the failed group/unwritten batch", p.ArxivID, row, in1, in2)
				}
			}
			if len(sink.papers) != len(asc) || minute(st.w["cs.LG"]) != 13 {
				t.Errorf("%d papers rows, W=%d; want one row per paper (%d), W=13", len(sink.papers), minute(st.w["cs.LG"]), len(asc))
			}
		})
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, st, sink := fixture(feeds{"cs.LG": {entries: []db.PaperRecord{at("d", 13), at("c", 12), at("b", 12), at("a", 11)}}}, 10, map[string]int{"cs.LG": 10})
	sink.afterAck = func(n int) {
		if n == 2 {
			cancel()
		}
	}
	// a, b acked, c (b's group) not: W = a's 11, written although ctx is cancelled.
	if err := l.Run(ctx); err != nil || strings.Join(sink.acked, " ") != "a b" || minute(st.w["cs.LG"]) != 11 || len(st.writes) != 1 {
		t.Errorf("Run = %v, acked %q, W=%d, writes %q; want nil, \"a b\", W=11 written once", err, sink.acked, minute(st.w["cs.LG"]), st.writes)
	}

	t.Run("waits Interval between passes, nil on cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var passes []time.Time
		f := &fakeFeed{entries: feed(9)}
		f.cancel = func() {
			if passes = append(passes, time.Now()); len(passes) == 2 {
				cancel()
			}
		}
		l, _, _ := fixture(feeds{"cs.LG": f}, 10, map[string]int{"cs.LG": 10})
		l.cfg.Interval = 30 * time.Millisecond
		if err := l.Run(ctx); err != nil || len(passes) != 2 || passes[1].Sub(passes[0]) < 30*time.Millisecond {
			t.Errorf("Run = %v after passes at %v; want nil after 2 passes one Interval apart", err, passes)
		}
	})
	t.Run("Once returns the pass error", func(t *testing.T) {
		l, _, _ := fixture(feeds{"cs.LG": {entries: feed(15), err: errors.New("arxiv 503")}}, 10, map[string]int{"cs.LG": 10})
		l.cfg.Once = true
		// One "cat:" prefix (RunOnce's), not "cat:cs.LG: cat:cs.LG page ..." (ING-6c AC11c).
		if err := l.Run(context.Background()); err == nil || err.Error() != "cat:cs.LG: page start=0: arxiv 503" {
			t.Errorf("Run with Once = %v, want the pass error \"cat:cs.LG: page start=0: arxiv 503\"", err)
		}
	})
}

func TestRunOnceCrossListed(t *testing.T) {
	// p14 is in both categories; cs.AI runs first and publishes it as its 2nd publish.
	for _, c := range []struct {
		name              string
		failPublish       int
		acked, err        string
		upserts, wAI, wLG int
	}{
		{"published once per pass", 0, "p12 p14 p13", "", 3, 14, 14},
		// M9 (acked set before the ack): cs.LG would skip p14 and still advance W to 14 over an unpublished paper.
		{"cs.AI's publish of p14 fails: cs.LG publishes it", 2, "p12 p13 p14", "cat:cs.AI: publish p14@1: broker down", 4, 12, 14},
	} {
		t.Run(c.name, func(t *testing.T) {
			l, st, sink := fixture(feeds{"cs.AI": {entries: feed(14, 12, 9)}, "cs.LG": {entries: feed(14, 13, 8)}}, 10, map[string]int{"cs.AI": 10, "cs.LG": 10})
			sink.failPublish = c.failPublish
			err := l.RunOnce(context.Background())
			if (c.err == "") != (err == nil) || (err != nil && err.Error() != c.err) {
				t.Errorf("RunOnce = %v, want %q", err, c.err)
			}
			got := strings.Join(sink.acked, " ")
			if !slices.Contains(sink.acked, "p14") && !st.w["cs.LG"].Before(t0.Add(14*time.Minute)) {
				t.Errorf("p14 never acked but W cs.LG=%d covers it", minute(st.w["cs.LG"]))
			}
			if got != c.acked || sink.upserts != c.upserts || minute(st.w["cs.AI"]) != c.wAI || minute(st.w["cs.LG"]) != c.wLG {
				t.Errorf("acked %q, %d upserts, W cs.AI=%d cs.LG=%d; want %q, %d, %d, %d",
					got, sink.upserts, minute(st.w["cs.AI"]), minute(st.w["cs.LG"]), c.acked, c.upserts, c.wAI, c.wLG)
			}
		})
	}
}

func TestRunOnceCancelledAfterFirstCategory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ai, lg := &fakeFeed{entries: feed(14, 12, 9)}, &fakeFeed{entries: feed(15, 13, 8)}
	l, st, sink := fixture(feeds{"cs.AI": ai, "cs.LG": lg}, 10, map[string]int{"cs.AI": 10, "cs.LG": 10})
	sink.afterAck = func(n int) {
		if n == 2 { // cs.AI's whole batch (p12 p14) acked
			cancel()
		}
	}
	err := l.RunOnce(ctx)
	// The exact text pins RunOnce's own guard: without it, cs.LG still runs and walk fails with "cat:cs.LG: context canceled".
	if !errors.Is(err, context.Canceled) || err.Error() != "context canceled" || len(lg.calls) != 0 || minute(st.w["cs.AI"]) != 14 || minute(st.w["cs.LG"]) != 10 {
		t.Errorf("RunOnce = %v, cs.LG %d feed requests, W cs.AI=%d cs.LG=%d; want exactly \"context canceled\", 0 requests, 14, 10",
			err, len(lg.calls), minute(st.w["cs.AI"]), minute(st.w["cs.LG"]))
	}
}

func TestRunOnceWatermarkReadError(t *testing.T) {
	ai, lg := &fakeFeed{entries: feed(14, 12, 9)}, &fakeFeed{entries: feed(15, 13, 8)}
	l, st, sink := fixture(feeds{"cs.AI": ai, "cs.LG": lg}, 10, map[string]int{"cs.LG": 10})
	st.readErr["cs.AI"] = errors.New("connection refused")
	if err := l.RunOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "cat:cs.AI: connection refused") {
		t.Errorf("RunOnce = %v, want an error naming cs.AI", err)
	}
	if len(ai.calls) != 0 || slices.Contains(st.writes, "cs.AI") {
		t.Errorf("cs.AI: %d feed requests, writes %q; want 0 requests, no AdvanceWatermark", len(ai.calls), st.writes)
	}
	if got := strings.Join(sink.acked, " "); got != "p13 p15" || sink.upserts != 2 || len(lg.calls) != 1 || minute(st.w["cs.LG"]) != 15 {
		t.Errorf("acked %q, %d upserts, W cs.LG=%d; want only cs.LG's \"p13 p15\", 2 upserts, W=15", got, sink.upserts, minute(st.w["cs.LG"]))
	}
}

func TestDiscoveredPayload(t *testing.T) {
	// Golden: the map Crawl built inline before the extraction, for the same record.
	p := db.PaperRecord{ArxivID: "2611.21608", Version: 2, Title: "T", Abstract: "A", Authors: []string{"Ada", "Alan"},
		Categories: []string{"cs.LG", "cs.AI"}, PublishedAt: time.Date(2026, 11, 16, 18, 59, 38, 5, time.UTC), SourceURL: "https://arxiv.org/pdf/2611.21608v2.pdf"}
	want := map[string]interface{}{
		"arxiv_id": "2611.21608", "version": 2, "title": "T", "abstract": "A", "authors": []string{"Ada", "Alan"},
		"categories": []string{"cs.LG", "cs.AI"}, "published_at": "2026-11-16T18:59:38Z", "source_url": "https://arxiv.org/pdf/2611.21608v2.pdf",
	}
	if got := discoveredPayload(&p); !reflect.DeepEqual(got, want) {
		t.Errorf("discoveredPayload = %#v, want %#v", got, want)
	}
}
