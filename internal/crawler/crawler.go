package crawler

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"arxivlens/internal/arxiv"
	"arxivlens/internal/db"
	"arxivlens/internal/kafka"
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
	database  *db.DB
	producer  *kafka.Producer
	client    *http.Client
	rateLimit time.Duration
}

func NewCrawler(database *db.DB, producer *kafka.Producer, rateLimit time.Duration) *Crawler {
	if rateLimit == 0 {
		rateLimit = 3 * time.Second
	}
	return &Crawler{
		database:  database,
		producer:  producer,
		client:    &http.Client{Timeout: 30 * time.Second},
		rateLimit: rateLimit,
	}
}

// CrawlQueries fetches recent papers matching query and publishes events
func (c *Crawler) Crawl(ctx context.Context, searchQuery string, limit int) (int, error) {
	log.Printf("[Crawler] Starting crawl query=%q limit=%d (rate-limit=%v)", searchQuery, limit, c.rateLimit)

	baseURL := "http://export.arxiv.org/api/query"
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

		req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
		if err != nil {
			return totalDiscovered, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("User-Agent", "ArxivLens/1.0 (Research Pipeline; mailto:contact@arxivlens.local)")

		resp, err := c.client.Do(req)
		if err != nil {
			return totalDiscovered, fmt.Errorf("http request failed: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return totalDiscovered, fmt.Errorf("failed to read response body: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return totalDiscovered, fmt.Errorf("arxiv API returned status: %d", resp.StatusCode)
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
			abstract := paperRec.Abstract
			authors := paperRec.Authors
			categories := paperRec.Categories
			pubTime := paperRec.PublishedAt
			pdfURL := paperRec.SourceURL

			// 1. Natural-key upsert into database (Layer 1 idempotency)
			_, err := c.database.UpsertDiscoveredPaper(ctx, paperRec)
			if err != nil {
				log.Printf("[Crawler] Warning: Failed to upsert paper %s: %v", arxivID, err)
			}

			// 2. Publish envelope to papers.discovered (key = arxiv_id)
			payload := map[string]interface{}{
				"arxiv_id":     arxivID,
				"version":      version,
				"title":        title,
				"abstract":     abstract,
				"authors":      authors,
				"categories":   categories,
				"published_at": pubTime.Format(time.RFC3339),
				"source_url":   pdfURL,
			}

			envelope := events.NewEventEnvelope(events.TopicPapersDiscovered, arxivID, version, payload)
			if err := c.producer.Publish(ctx, events.TopicPapersDiscovered, envelope); err != nil {
				log.Printf("[Crawler] Failed to publish event for %s: %v", arxivID, err)
				continue
			}

			totalDiscovered++
			log.Printf("[Crawler] Discovered & Published: %s (v%d) — %q", arxivID, version, title)
		}

		// arXiv rate limiting rule: ~1 request every 3 seconds
		log.Printf("[Crawler] Rate limit sleep (%v)...", c.rateLimit)
		select {
		case <-ctx.Done():
			return totalDiscovered, ctx.Err()
		case <-time.After(c.rateLimit):
		}
	}

	log.Printf("[Crawler] Crawl finished. Total discovered: %d", totalDiscovered)
	return totalDiscovered, nil
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
		// An entry without a version keeps the unversioned URL: the version is unknown, and the
		// stored 1 is only a default.
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
