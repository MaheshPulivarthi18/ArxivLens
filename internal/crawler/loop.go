package crawler

// Scheduled crawl loop (ING-6, design docs/design/ING-6.md in arxivlens-agents). Per category and pass: walk
// newest-first down to the watermark (decide), emit the oldest batch oldest-first, then advance the watermark
// past the papers that were upserted and acked (act). The CLI comes in ING-6c.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"arxivlens/internal/arxiv"
	"arxivlens/internal/db"
	"arxivlens/internal/kafka"
	"arxivlens/shared/events"
)

// pageFetcher returns one page of a newest-first (sortBy=submittedDate, descending) listing of query.
type pageFetcher interface {
	Page(ctx context.Context, query string, start, n int) ([]db.PaperRecord, error)
}
type paperUpserter interface {
	UpsertDiscoveredPaper(ctx context.Context, p *db.PaperRecord) (int64, error)
}
type eventPublisher interface {
	Publish(ctx context.Context, topic string, e events.EventEnvelope) error
}
type watermarkStore interface {
	Watermark(ctx context.Context, category string) (t time.Time, ok bool, err error)
	AdvanceWatermark(ctx context.Context, category string, t time.Time) error
}

var (
	_ paperUpserter  = (*db.DB)(nil)
	_ watermarkStore = (*db.DB)(nil)
	_ eventPublisher = (*kafka.Producer)(nil)
)

// LoopConfig configures the crawl loop. The CLI (ING-6c) validates it: LimitPerRun > 0, MaxScan >= LimitPerRun.
type LoopConfig struct {
	Categories           []string
	Interval             time.Duration
	LimitPerRun, MaxScan int
	Once                 bool
}

// maxPageSize caps one arXiv API request of the loop.
const maxPageSize = 100

// walkStats describes one walk. OutOfOrder counts entries whose published time is later than the previous
// (dated) entry's in the same walk: the walk assumes the feed is newest-first and this checks it.
type walkStats struct{ Pages, Scanned, OutOfOrder int }

// walk lists cat:category newest-first and returns the papers published strictly after w (all of them on a
// first run, hasW false), deduped by arXiv ID. It stops after a page whose last dated entry is <= w, after a
// short page, or, on a first run, once limit entries were requested. With a watermark and no boundary within
// maxScan entries it fails and returns nothing, so a backlog is never silently skipped.
func walk(ctx context.Context, f pageFetcher, category string, w time.Time, hasW bool, limit, maxScan int) ([]db.PaperRecord, walkStats, error) {
	var st walkStats
	var out []db.PaperRecord
	var prev time.Time
	seen := map[string]bool{}
	pageSize := min(max(limit, 1), maxPageSize)
	for start := 0; ; start += pageSize {
		if err := ctx.Err(); err != nil {
			return nil, st, err
		}
		n := pageSize
		if !hasW {
			if start >= limit {
				break
			}
			n = min(pageSize, limit-start)
		}
		page, err := f.Page(ctx, "cat:"+category, start, n)
		if err != nil {
			return nil, st, fmt.Errorf("cat:%s page start=%d: %w", category, start, err)
		}
		st.Pages++
		st.Scanned += len(page)
		var last time.Time // last (oldest) dated entry of this page
		for _, p := range page {
			if p.PublishedAt.IsZero() {
				log.Printf("[CrawlLoop] cat=%s: ignoring %s, no parsable published time", category, p.ArxivID)
				continue
			}
			if !prev.IsZero() && p.PublishedAt.After(prev) {
				st.OutOfOrder++
			}
			prev, last = p.PublishedAt, p.PublishedAt
			if (hasW && !p.PublishedAt.After(w)) || seen[p.ArxivID] {
				continue
			}
			seen[p.ArxivID] = true
			out = append(out, p)
		}
		if len(page) < n || (hasW && !last.IsZero() && !last.After(w)) {
			break
		}
		if hasW && st.Scanned >= maxScan {
			return nil, st, fmt.Errorf("backlog in cat:%s exceeds --max-scan %d; raise --max-scan once or backfill with arxivlens crawl", category, maxScan)
		}
	}
	return out, st, nil
}

// selectBatch sorts candidates ascending by (published, arXiv ID) and returns the oldest limit of them,
// extended to the end of the last group of equal published times, so a batch never splits such a group.
func selectBatch(candidates []db.PaperRecord, limit int) []db.PaperRecord {
	sorted := slices.Clone(candidates)
	slices.SortFunc(sorted, func(a, b db.PaperRecord) int {
		if c := a.PublishedAt.Compare(b.PublishedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ArxivID, b.ArxivID)
	})
	n := max(limit, 1)
	if len(sorted) <= n {
		return sorted
	}
	for n < len(sorted) && sorted[n].PublishedAt.Equal(sorted[n-1].PublishedAt) {
		n++
	}
	return sorted[:n]
}

// watermarkAfter returns the watermark earned by emitting batch[:k] of an ascending batch: the published time of
// the last emitted paper whose whole group of equal published times was emitted. ok is false if there is none.
// selectBatch never splits a group, so k == len(batch) earns the batch's last published time.
func watermarkAfter(batch []db.PaperRecord, k int) (time.Time, bool) {
	for ; k > 0; k-- {
		if k == len(batch) || batch[k].PublishedAt.After(batch[k-1].PublishedAt) {
			return batch[k-1].PublishedAt, true
		}
	}
	return time.Time{}, false
}

// watermarkWriteTimeout bounds the watermark write, which runs even after ctx is cancelled (SIGTERM).
const watermarkWriteTimeout = 10 * time.Second

// Loop is the scheduled crawler: every Interval, one pass over all categories.
type Loop struct {
	cfg  LoopConfig
	feed pageFetcher
	db   paperUpserter
	pub  eventPublisher
	wm   watermarkStore
}

// NewLoop returns a crawl loop. *db.DB is both u and w; *kafka.Producer is p; NewArxivFeed is f.
func NewLoop(cfg LoopConfig, f pageFetcher, u paperUpserter, p eventPublisher, w watermarkStore) *Loop {
	return &Loop{cfg: cfg, feed: f, db: u, pub: p, wm: w}
}

// Run runs a pass, then waits Interval from the end of the pass, until ctx is done; it returns nil then (a
// signal is a clean stop). With Once it returns after one pass with that pass's error.
func (l *Loop) Run(ctx context.Context) error {
	for {
		err := l.RunOnce(ctx)
		switch {
		case ctx.Err() != nil:
			log.Printf("[CrawlLoop] stopped")
			return nil
		case l.cfg.Once:
			return err
		case err != nil:
			log.Printf("[CrawlLoop] pass failed, next pass in %v: %v", l.cfg.Interval, err)
		}
		select {
		case <-ctx.Done(): // the next RunOnce returns at once and Run returns nil
		case <-time.After(l.cfg.Interval):
		}
	}
}

// RunOnce makes one pass over all categories and returns the joined errors of the categories that failed.
// A paper (arxiv_id@version) acked for one category in this pass isn't published again for the next one.
func (l *Loop) RunOnce(ctx context.Context) error {
	acked := map[string]bool{}
	var errs []error
	for _, c := range l.cfg.Categories {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		if err := l.runCategory(ctx, c, acked); err != nil {
			errs = append(errs, fmt.Errorf("cat:%s: %w", c, err))
		}
	}
	return errors.Join(errs...)
}

// runCategory walks, emits the batch oldest-first and advances the watermark to the group-complete prefix of
// what was upserted and acked, also when it stopped early on an error or a cancelled ctx.
func (l *Loop) runCategory(ctx context.Context, category string, acked map[string]bool) (err error) {
	var st walkStats
	candidates, emitted, wmNote := 0, 0, "(unchanged)"
	w, hasW, err := l.wm.Watermark(ctx, category)
	readErr := err != nil
	defer func() {
		wm := "none"
		if readErr {
			wm = "unknown"
		} else if hasW {
			wm = w.UTC().Format(time.RFC3339)
		}
		tail := ""
		if err != nil {
			tail = " error=" + err.Error()
		}
		log.Printf("[CrawlLoop] cat=%s pages=%d scanned=%d candidates=%d emitted=%d backlog=%d out_of_order=%d watermark=%s %s%s",
			category, st.Pages, st.Scanned, candidates, emitted, candidates-emitted, st.OutOfOrder, wm, wmNote, tail)
	}()
	if readErr {
		return err // never "no watermark" (a first run would skip the gap): skip the category
	}
	var all []db.PaperRecord
	all, st, err = walk(ctx, l.feed, category, w, hasW, l.cfg.LimitPerRun, l.cfg.MaxScan)
	if err != nil {
		return err
	}
	candidates = len(all)
	batch := selectBatch(all, l.cfg.LimitPerRun)
	var emitErr error
	for ; emitted < len(batch); emitted++ {
		if emitErr = ctx.Err(); emitErr != nil {
			break
		}
		if emitErr = l.emit(ctx, &batch[emitted], acked); emitErr != nil {
			break
		}
	}
	if next, ok := watermarkAfter(batch, emitted); ok { // candidates are > W, so next is too
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), watermarkWriteTimeout)
		defer cancel()
		if werr := l.wm.AdvanceWatermark(wctx, category, next); werr != nil {
			wmNote = "(write failed)"
			return errors.Join(emitErr, werr)
		}
		w, hasW, wmNote = next, true, "(advanced)"
	}
	return emitErr
}

// emit upserts p (L1 natural key) and publishes its papers.discovered event, unless this pass already did.
func (l *Loop) emit(ctx context.Context, p *db.PaperRecord, acked map[string]bool) error {
	key := fmt.Sprintf("%s@%d", p.ArxivID, p.Version)
	if acked[key] {
		return nil
	}
	if _, err := l.db.UpsertDiscoveredPaper(ctx, p); err != nil {
		return fmt.Errorf("upsert %s: %w", key, err)
	}
	e := events.NewEventEnvelope(events.TopicPapersDiscovered, p.ArxivID, p.Version, discoveredPayload(p))
	if err := l.pub.Publish(ctx, events.TopicPapersDiscovered, e); err != nil {
		return fmt.Errorf("publish %s: %w", key, err)
	}
	acked[key] = true
	return nil
}

type arxivFeed struct {
	client *http.Client
	lim    *arxiv.Limiter
	retry  arxiv.Retry
}

// NewArxivFeed returns the arXiv API page fetcher of the loop. Every request and every retry waits for lim,
// which must be the one limiter of the process (nil: a new DefaultInterval limiter).
func NewArxivFeed(client *http.Client, lim *arxiv.Limiter) pageFetcher {
	if lim == nil {
		lim = arxiv.NewLimiter(arxiv.DefaultInterval)
	}
	return &arxivFeed{client: client, lim: lim, retry: arxiv.DefaultRetry}
}

// Page sends the same query string as Crawl, so CI's arXiv replay store serves it.
func (f *arxivFeed) Page(ctx context.Context, query string, start, n int) ([]db.PaperRecord, error) {
	params := url.Values{}
	params.Set("search_query", query)
	params.Set("start", strconv.Itoa(start))
	params.Set("max_results", strconv.Itoa(n))
	params.Set("sortBy", "submittedDate")
	params.Set("sortOrder", "descending")
	body, err := arxiv.GetAPI(ctx, f.client, f.lim, arxiv.APIURL()+"?"+params.Encode(), arxiv.UserAgent("Crawl Loop"), f.retry)
	if err != nil {
		return nil, err
	}
	return parseAtomFeed(body)
}

var categoryRE = regexp.MustCompile(`^[a-z-]+(\.[A-Za-z-]+)?$`)

// ParseCategories parses a comma-separated list of arXiv categories (e.g. "cs.AI, cs.LG"): trimmed, deduped,
// order kept. Each must look like an arXiv category, so nothing else can reach the search query.
func ParseCategories(s string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(s, ",") {
		c := strings.TrimSpace(part)
		if !categoryRE.MatchString(c) {
			return nil, fmt.Errorf("invalid arXiv category %q in %q (want e.g. cs.AI, hep-th, math.CO)", c, s)
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out, nil
}
