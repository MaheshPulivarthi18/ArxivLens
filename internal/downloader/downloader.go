package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"arxivlens/internal/arxiv"
	"arxivlens/internal/db"
	"arxivlens/internal/kafka"
	"arxivlens/internal/storage"
	"arxivlens/shared/events"
	"github.com/twmb/franz-go/pkg/kgo"
)

type Downloader struct {
	database *db.DB
	storage  *storage.Storage
	producer *kafka.Producer
	client   *http.Client
	// limiter spaces this process's arXiv PDF requests (Technical PRD §9).
	limiter *arxiv.Limiter
}

func NewDownloader(database *db.DB, storage *storage.Storage, producer *kafka.Producer) *Downloader {
	return &Downloader{
		database: database,
		storage:  storage,
		producer: producer,
		client:   &http.Client{Timeout: 60 * time.Second},
		limiter:  arxiv.NewLimiter(arxiv.DefaultInterval),
	}
}

func (d *Downloader) HandleRecord(ctx context.Context, record *kgo.Record) error {
	var envelope events.EventEnvelope
	if err := json.Unmarshal(record.Value, &envelope); err != nil {
		return fmt.Errorf("failed to unmarshal envelope: %w", err)
	}

	arxivID := envelope.PaperID
	version := envelope.Version
	if version == 0 {
		version = 1
	}

	log.Printf("[Downloader] Processing paper %s (v%d, event_id=%s)", arxivID, version, envelope.EventID)

	storageKey := fmt.Sprintf("raw/%s_v%d.pdf", arxivID, version)

	// Check if already in MinIO (Layer 1 idempotency)
	exists, err := d.storage.ObjectExists(ctx, storageKey)
	if err != nil {
		log.Printf("[Downloader] Warning checking storage: %v", err)
	}

	var pdfBytes []byte
	var sha256Hex string

	if exists {
		log.Printf("[Downloader] PDF %s already in storage, reading for sha256 verification...", storageKey)
		obj, err := d.storage.DownloadPDF(ctx, storageKey)
		if err == nil {
			pdfBytes, _ = io.ReadAll(obj)
			obj.Close()
			h := sha256.Sum256(pdfBytes)
			sha256Hex = hex.EncodeToString(h[:])
		}
	}

	if len(pdfBytes) == 0 {
		// Download from arXiv PDF endpoint
		pdfURL := fmt.Sprintf("https://arxiv.org/pdf/%s.pdf", arxivID)
		pdfBytes, err = d.fetchPDF(ctx, pdfURL)
		if err != nil {
			return err
		}

		h := sha256.Sum256(pdfBytes)
		sha256Hex = hex.EncodeToString(h[:])

		// Upload to MinIO object store claim-check
		err = d.storage.UploadPDF(ctx, storageKey, bytes.NewReader(pdfBytes), int64(len(pdfBytes)))
		if err != nil {
			return fmt.Errorf("failed to upload PDF to minio: %w", err)
		}
		log.Printf("[Downloader] Uploaded PDF to MinIO: %s (%d bytes, sha256=%s)", storageKey, len(pdfBytes), sha256Hex[:16])
	}

	// Update database status (Layer 1)
	err = d.database.UpdatePaperDownloaded(ctx, arxivID, version, storageKey, sha256Hex)
	if err != nil {
		return fmt.Errorf("failed to update paper status in db: %w", err)
	}

	// Publish envelope to papers.downloaded
	downloadedPayload := map[string]interface{}{
		"arxiv_id":        arxivID,
		"version":         version,
		"pdf_storage_key": storageKey,
		"sha256":          sha256Hex,
		"bytes":           len(pdfBytes),
		"storage_bucket":  d.storage.Bucket,
	}

	downEnvelope := events.NewEventEnvelope(events.TopicPapersDownloaded, arxivID, version, downloadedPayload)
	downEnvelope.Traceparent = envelope.Traceparent

	if err := d.producer.Publish(ctx, events.TopicPapersDownloaded, downEnvelope); err != nil {
		return fmt.Errorf("failed to publish papers.downloaded event: %w", err)
	}

	log.Printf("[Downloader] Successfully processed & published papers.downloaded for %s", arxivID)
	return nil
}

// fetchPDF waits for the arXiv limiter and GETs pdfURL with the identifying User-Agent.
func (d *Downloader) fetchPDF(ctx context.Context, pdfURL string) ([]byte, error) {
	if err := d.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", pdfURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create pdf request: %w", err)
	}
	req.Header.Set("User-Agent", arxiv.UserAgent("Downloader Pipeline"))

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download pdf from %s: %w", pdfURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("arxiv pdf download failed with status %d", resp.StatusCode)
	}

	pdfBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read pdf body: %w", err)
	}
	return pdfBytes, nil
}
