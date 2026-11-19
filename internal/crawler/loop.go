package crawler

// Scheduled crawl loop (ING-6, design docs/design/ING-6.md in arxivlens-agents). This file holds the "decide" half:
// walk a category newest-first down to its watermark, then pick the oldest batch to emit. Emit, watermark
// advance and the loop itself come in ING-6b; the CLI in ING-6c.

import (
	"context"
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

// *db.DB also satisfies watermarkStore once ING-6a1 (internal/db/watermarks.go) is merged; 6b asserts it.
var _, _ = paperUpserter((*db.DB)(nil)), eventPublisher((*kafka.Producer)(nil))

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
