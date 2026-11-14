package query

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"arxivlens/internal/arxiv"
	"arxivlens/internal/config"
	"arxivlens/internal/db"
	"github.com/minio/minio-go/v7"
)

type IngestResult struct {
	Status     string `json:"status"`
	ArxivID    string `json:"arxiv_id"`
	Version    int    `json:"version"`
	Title      string `json:"title"`
	Sections   int    `json:"sections"`
	Chunks     int    `json:"chunks"`
	Embeddings int    `json:"embeddings"`
	Error      string `json:"error,omitempty"`
}

// ingestDB and objectPutter are the parts of *db.DB and *minio.Client the ingester uses, so
// IngestPaper can be unit-tested with fakes.
type ingestDB interface {
	UpsertDiscoveredPaper(ctx context.Context, p *db.PaperRecord) (int64, error)
	UpdatePaperDownloaded(ctx context.Context, arxivID string, version int, storageKey, sha256 string) error
}

type objectPutter interface {
	PutObject(ctx context.Context, bucket, object string, r io.Reader, size int64, opts minio.PutObjectOptions) (minio.UploadInfo, error)
}

type PaperIngester struct {
	database    ingestDB
	minioClient objectPutter // nil: skip the MinIO upload
	minioBucket string
	embedderURL string
	httpClient  *http.Client
	// ingestClient is used only for the embedder's synchronous /internal/ingest (chunk + embed),
	// which can take minutes on CPU; arXiv metadata fetches keep httpClient's shorter timeout.
	ingestClient *http.Client
	// limiter spaces every arXiv request this process's ingester makes (Technical PRD §9).
	limiter *arxiv.Limiter
	// pdfLimits bounds PDF downloads (stall/total/size); httpClient's Timeout applies to metadata only.
	pdfLimits arxiv.PDFLimits
}

// doArxiv waits for the arXiv limiter, sets the identifying User-Agent and sends req.
func (ing *PaperIngester) doArxiv(req *http.Request) (*http.Response, error) {
	if err := ing.limiter.Wait(req.Context()); err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", arxiv.UserAgent("Query Ingester"))
	return ing.httpClient.Do(req)
}

// NewPaperIngester builds an on-demand ingester. ingestTimeout bounds the /internal/ingest call;
// zero or negative means config.DefaultIngestTimeout.
func NewPaperIngester(database *db.DB, minioClient *minio.Client, minioBucket, embedderURL string, ingestTimeout time.Duration) *PaperIngester {
	if embedderURL == "" {
		embedderURL = "http://localhost:8081"
	}
	if ingestTimeout <= 0 {
		ingestTimeout = config.DefaultIngestTimeout
	}
	ing := &PaperIngester{
		database:    database,
		minioBucket: minioBucket,
		embedderURL: strings.TrimRight(embedderURL, "/"),
		httpClient: &http.Client{
			Timeout: 45 * time.Second,
		},
		ingestClient: &http.Client{Timeout: ingestTimeout},
		limiter:      arxiv.NewLimiter(arxiv.DefaultInterval),
		pdfLimits:    arxiv.DefaultPDFLimits,
	}
	if minioClient != nil { // a nil *minio.Client must stay a nil interface
		ing.minioClient = minioClient
	}
	return ing
}

// ExtractCleanArxivID extracts standard format "2608.20154" from any string or URL
func ExtractCleanArxivID(input string) (string, error) {
	input = strings.TrimSpace(input)
	re := regexp.MustCompile(`(\d{4}\.\d{4,5})`)
	match := re.FindString(input)
	if match != "" {
		return match, nil
	}
	return "", fmt.Errorf("could not extract valid arXiv ID from: %s", input)
}

type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID         string         `xml:"id"`
	Published  string         `xml:"published"`
	Title      string         `xml:"title"`
	Summary    string         `xml:"summary"`
	Authors    []atomAuthor   `xml:"author"`
	Categories []atomCategory `xml:"category"`
	Links      []atomLink     `xml:"link"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomCategory struct {
	Term string `xml:"term,attr"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

func (ing *PaperIngester) IngestPaper(ctx context.Context, input string) (*IngestResult, error) {
	arxivID, err := ExtractCleanArxivID(input)
	if err != nil {
		return nil, err
	}

	log.Printf("[Ingester] Initiating on-demand ingestion for arXiv ID: %s", arxivID)

	// Step 1: Query arXiv Atom API for metadata
	queryURL := fmt.Sprintf("http://export.arxiv.org/api/query?id_list=%s", arxivID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create arxiv request: %w", err)
	}

	resp, err := ing.doArxiv(req)
	if err != nil {
		return nil, fmt.Errorf("arxiv API request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read arxiv response: %w", err)
	}

	var feed atomFeed
	if err := xml.Unmarshal(bodyBytes, &feed); err != nil {
		return nil, fmt.Errorf("failed to parse arxiv XML: %w", err)
	}

	if len(feed.Entries) == 0 || feed.Entries[0].Title == "" {
		return nil, fmt.Errorf("paper not found on arXiv: %s", arxivID)
	}

	entry := feed.Entries[0]
	// The stored version must be the one whose PDF is chunked, so take it from the entry and fetch
	// exactly that version. No version means no ingest: defaulting would label the latest PDF "v1".
	_, version, ok := arxiv.SplitVersion(entry.ID)
	if !ok {
		return nil, fmt.Errorf("arXiv entry ID %q for %s has no valid version; not ingesting", entry.ID, arxivID)
	}
	cleanTitle := strings.TrimSpace(strings.ReplaceAll(entry.Title, "\n", " "))
	cleanAbstract := strings.TrimSpace(strings.ReplaceAll(entry.Summary, "\n", " "))

	var authors []string
	for _, a := range entry.Authors {
		if a.Name != "" {
			authors = append(authors, a.Name)
		}
	}

	var categories []string
	for _, c := range entry.Categories {
		if c.Term != "" {
			categories = append(categories, c.Term)
		}
	}

	var pubTime time.Time
	if t, err := time.Parse(time.RFC3339, entry.Published); err == nil {
		pubTime = t
	} else {
		pubTime = time.Now().UTC()
	}

	pdfStorageKey := fmt.Sprintf("raw/%s_v%d.pdf", arxivID, version)
	sourceURL := arxiv.PDFURL(arxivID, version)

	// Step 2: Download PDF and Stream to MinIO
	log.Printf("[Ingester] Downloading PDF from %s...", sourceURL)
	if err := ing.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	// Stall-based limits, not httpClient's 45 s total: large PDFs arrive slowly but steadily.
	pdfBytes, err := arxiv.FetchPDF(ctx, ing.httpClient, sourceURL, arxiv.UserAgent("Query Ingester"), ing.pdfLimits)
	if err != nil {
		return nil, err
	}

	hasher := sha256.New()
	hasher.Write(pdfBytes)
	sha256Hex := hex.EncodeToString(hasher.Sum(nil))

	// Upload to MinIO
	if ing.minioClient != nil {
		_, err = ing.minioClient.PutObject(ctx, ing.minioBucket, pdfStorageKey, bytes.NewReader(pdfBytes), int64(len(pdfBytes)), minio.PutObjectOptions{
			ContentType: "application/pdf",
		})
		if err != nil {
			return nil, fmt.Errorf("failed to upload PDF to MinIO: %w", err)
		}
	}

	// Step 3: Insert / Update in PostgreSQL
	paperRecord := &db.PaperRecord{
		ArxivID:       arxivID,
		Version:       version,
		Title:         cleanTitle,
		Abstract:      cleanAbstract,
		Authors:       authors,
		Categories:    categories,
		PublishedAt:   pubTime,
		SourceURL:     sourceURL,
		PDFStorageKey: pdfStorageKey,
		SHA256:        sha256Hex,
		Status:        "downloaded",
	}

	if _, err := ing.database.UpsertDiscoveredPaper(ctx, paperRecord); err != nil {
		return nil, fmt.Errorf("failed to save discovered paper in DB: %w", err)
	}
	if err := ing.database.UpdatePaperDownloaded(ctx, arxivID, version, pdfStorageKey, sha256Hex); err != nil {
		return nil, fmt.Errorf("failed to update downloaded paper in DB: %w", err)
	}

	// Step 4: Call Python Embedder / Processor API for Typography Chunking & GPU Embedding
	log.Printf("[Ingester] Triggering chunking and GPU embedding for %s...", arxivID)
	processReqPayload := map[string]interface{}{
		"arxiv_id":        arxivID,
		"version":         version,
		"pdf_storage_key": pdfStorageKey,
	}
	payloadBytes, _ := json.Marshal(processReqPayload)

	apiResp, err := ing.ingestClient.Post(fmt.Sprintf("%s/internal/ingest", ing.embedderURL), "application/json", bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("embedder API call failed: %w", err)
	}
	defer apiResp.Body.Close()

	if apiResp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(apiResp.Body)
		return nil, fmt.Errorf("embedder returned error %d: %s", apiResp.StatusCode, string(errBody))
	}

	var ingestStats struct {
		Status     string `json:"status"`
		ArxivID    string `json:"arxiv_id"`
		Sections   int    `json:"sections"`
		Chunks     int    `json:"chunks"`
		Embeddings int    `json:"embeddings"`
	}
	_ = json.NewDecoder(apiResp.Body).Decode(&ingestStats)

	log.Printf("[Ingester] Successfully indexed %s! (%d sections, %d chunks, %d embeddings)", arxivID, ingestStats.Sections, ingestStats.Chunks, ingestStats.Embeddings)

	return &IngestResult{
		Status:     "success",
		ArxivID:    arxivID,
		Version:    version,
		Title:      cleanTitle,
		Sections:   ingestStats.Sections,
		Chunks:     ingestStats.Chunks,
		Embeddings: ingestStats.Embeddings,
	}, nil
}

func (ing *PaperIngester) DiscoverAndIngest(ctx context.Context, query string) (*IngestResult, error) {
	encodedQuery := url.QueryEscape(query)
	searchURL := fmt.Sprintf("http://export.arxiv.org/api/query?search_query=all:%s&sortBy=relevance&sortOrder=descending&max_results=1", encodedQuery)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create search request: %w", err)
	}

	resp, err := ing.doArxiv(req)
	if err != nil {
		return nil, fmt.Errorf("failed to search arXiv: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var feed atomFeed
	if err := xml.Unmarshal(bodyBytes, &feed); err != nil {
		return nil, fmt.Errorf("failed to parse XML: %w", err)
	}

	if len(feed.Entries) == 0 || feed.Entries[0].ID == "" {
		return nil, fmt.Errorf("no papers found on arXiv matching: %s", query)
	}

	return ing.IngestPaper(ctx, feed.Entries[0].ID)
}
