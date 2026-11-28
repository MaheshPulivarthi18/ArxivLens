package crawler

import (
	"context"
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"arxivlens/internal/arxiv"
	"arxivlens/internal/db"
	"arxivlens/shared/events"
)

// Atom feed XML structs
type Feed struct {
	XMLName xml.Name `xml:"feed"`
	Entries []Entry  `xml:"entry"`
}

type Entry struct {
	ID         string     `xml:"id"`
	Published  string     `xml:"published"`
	Title      string     `xml:"title"`
	Summary    string     `xml:"summary"`
	Authors    []Author   `xml:"author"`
	Categories []Category `xml:"category"`
	Links      []Link     `xml:"link"`
}

type Author struct {
	Name string `xml:"name"`
}

type Category struct {
	Term string `xml:"term,attr"`
}

type Link struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type Crawler struct {
	database paperUpserter
	producer eventPublisher
	client   *http.Client
	lim      *arxiv.Limiter // every request, retries included, waits on it (Technical PRD §9: <= 1 request / 3 s)
	retry    arxiv.Retry    // per-page retry on 429/5xx/timeouts (arxiv.DefaultRetry)
}

// NewCrawler: a rateLimit below arxiv.DefaultInterval (3 s) is raised to it, so no caller can break arXiv's limit.
func NewCrawler(database paperUpserter, producer eventPublisher, rateLimit time.Duration) *Crawler {
	return &Crawler{
		database: database,
		producer: producer,
		client:   &http.Client{Timeout: 30 * time.Second},
		lim:      arxiv.NewLimiter(max(rateLimit, arxiv.DefaultInterval)),
		retry:    arxiv.DefaultRetry,
	}
}

// CrawlQueries fetches recent papers matching query and publishes events
func (c *Crawler) Crawl(ctx context.Context, searchQuery string, limit int) (int, error) {
	log.Printf("[Crawler] Starting crawl query=%q limit=%d (rate-limit=%v)", searchQuery, limit, c.lim.Interval())

	baseURL := arxiv.APIURL()
	pageSize := 20
	if limit < pageSize {
		pageSize = limit
	}

	totalDiscovered := 0

	for start := 0; start < limit; start += pageSize {
		select {
		case <-ctx.Done():
			return totalDiscovered, ctx.Err()
		default:
		}

		currLimit := pageSize
		if start+currLimit > limit {
			currLimit = limit - start
		}

		params := url.Values{}
		params.Set("search_query", searchQuery)
		params.Set("start", strconv.Itoa(start))
		params.Set("max_results", strconv.Itoa(currLimit))
		params.Set("sortBy", "submittedDate")
		params.Set("sortOrder", "descending")

		reqURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())
		log.Printf("[Crawler] Fetching page start=%d size=%d URL=%s", start, currLimit, reqURL)

		// Retries 429/5xx/timeouts with backoff (honouring Retry-After); every attempt waits on c.lim; a non-200 is never parsed.
		body, err := arxiv.GetAPI(ctx, c.client, c.lim, reqURL, arxiv.UserAgent("Research Pipeline"), c.retry)
		if err != nil {
			return totalDiscovered, fmt.Errorf("arxiv page start=%d: %w", start, err)
		}

		papers, err := parseAtomFeed(body)
		if err != nil {
			return totalDiscovered, err
		}

		if len(papers) == 0 {
			log.Printf("[Crawler] No more entries found.")
			break
		}

		for i := range papers {
			paperRec := &papers[i]
			arxivID := paperRec.ArxivID
			version := paperRec.Version
			title := paperRec.Title

			// 1. Natural-key upsert into database (Layer 1 idempotency)
			_, err := c.database.UpsertDiscoveredPaper(ctx, paperRec)
			if err != nil {
				log.Printf("[Crawler] Warning: Failed to upsert paper %s: %v", arxivID, err)
			}

			// 2. Publish envelope to papers.discovered (key = arxiv_id)
			envelope := events.NewEventEnvelope(events.TopicPapersDiscovered, arxivID, version, discoveredPayload(paperRec))
			if err := c.producer.Publish(ctx, events.TopicPapersDiscovered, envelope); err != nil {
				log.Printf("[Crawler] Failed to publish event for %s: %v", arxivID, err)
				continue
			}

			totalDiscovered++
			log.Printf("[Crawler] Discovered & Published: %s (v%d) — %q", arxivID, version, title)
		}
	}

	log.Printf("[Crawler] Crawl finished. Total discovered: %d", totalDiscovered)
	return totalDiscovered, nil
}

// discoveredPayload is the papers.discovered payload of p, shared by Crawl and the crawl loop.
func discoveredPayload(p *db.PaperRecord) map[string]interface{} {
	return map[string]interface{}{
		"arxiv_id":     p.ArxivID,
		"version":      p.Version,
		"title":        p.Title,
		"abstract":     p.Abstract,
		"authors":      p.Authors,
		"categories":   p.Categories,
		"published_at": p.PublishedAt.Format(time.RFC3339),
		"source_url":   p.SourceURL,
	}
}

// parseAtomFeed maps an arXiv Atom API response body to paper records.
func parseAtomFeed(body []byte) ([]db.PaperRecord, error) {
	var feed Feed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("failed to parse Atom XML: %w", err)
	}

	papers := make([]db.PaperRecord, 0, len(feed.Entries))
	for _, entry := range feed.Entries {
		// Extract arXiv ID and version (e.g., from http://arxiv.org/abs/2401.12345v2)
		arxivID, version, versioned := arxiv.SplitVersion(entry.ID)
		// An entry without a version is stored as version 1, and the downloader fetches v1
		// explicitly. Its source_url stays unversioned on purpose: the feed didn't say which
		// version it listed, so the row stays visible to the suspect-rows query.
		pdfURL := fmt.Sprintf("https://arxiv.org/pdf/%s.pdf", arxivID)
		if versioned {
			pdfURL = arxiv.PDFURL(arxivID, version)
		} else {
			version = 1
		}

		title := strings.Join(strings.Fields(entry.Title), " ")
		abstract := strings.Join(strings.Fields(entry.Summary), " ")

		var authors []string
		for _, a := range entry.Authors {
			if strings.TrimSpace(a.Name) != "" {
				authors = append(authors, strings.TrimSpace(a.Name))
			}
		}

		var categories []string
		for _, cat := range entry.Categories {
			if cat.Term != "" {
				categories = append(categories, cat.Term)
			}
		}

		var pubTime time.Time
		if entry.Published != "" {
			pubTime, _ = time.Parse(time.RFC3339, entry.Published)
		}

		papers = append(papers, db.PaperRecord{
			ArxivID:     arxivID,
			Version:     version,
			Title:       title,
			Abstract:    abstract,
			Authors:     authors,
			Categories:  categories,
			PublishedAt: pubTime,
			SourceURL:   pdfURL,
		})
	}
	return papers, nil
}
