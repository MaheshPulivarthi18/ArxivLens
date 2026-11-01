package events

import (
	"crypto/rand"
	"time"

	"github.com/oklog/ulid/v2"
)

// Standard topic names matching §2
const (
	TopicPapersDiscovered    = "papers.discovered"
	TopicPapersDiscoveredDLQ = "papers.discovered.dlq"
	TopicPapersDownloaded    = "papers.downloaded"
	TopicPapersDownloadedDLQ = "papers.downloaded.dlq"
	TopicPapersChunked       = "papers.chunked"
	TopicPapersChunkedDLQ    = "papers.chunked.dlq"
)

// EventEnvelope standard wrapper for all Kafka events
type EventEnvelope struct {
	EventID     string                 `json:"event_id"`
	EventType   string                 `json:"event_type"`
	OccurredAt  string                 `json:"occurred_at"`
	PaperID     string                 `json:"paper_id"`
	Version     int                    `json:"version"`
	Attempt     int                    `json:"attempt"`
	Traceparent string                 `json:"traceparent,omitempty"`
	Payload     map[string]interface{} `json:"payload"`
}

// NewEventEnvelope creates a new envelope with a freshly generated ULID
func NewEventEnvelope(eventType, paperID string, version int, payload map[string]interface{}) EventEnvelope {
	t := time.Now().UTC()
	entropy := ulid.Monotonic(rand.Reader, 0)
	id := ulid.MustNew(ulid.Timestamp(t), entropy).String()

	if payload == nil {
		payload = make(map[string]interface{})
	}

	return EventEnvelope{
		EventID:    id,
		EventType:  eventType,
		OccurredAt: t.Format(time.RFC3339Nano),
		PaperID:    paperID,
		Version:    version,
		Attempt:    0,
		Payload:    payload,
	}
}

// DLQPayload structure for dead letter queue events carrying full diagnostic context (§6)
type DLQPayload struct {
	OriginalTopic string                 `json:"original_topic"`
	Partition     int32                  `json:"partition"`
	Offset        int64                  `json:"offset"`
	OriginalKey   string                 `json:"original_key"`
	OriginalValue map[string]interface{} `json:"original_value"`
	ErrorType     string                 `json:"error_type"`
	ErrorMessage  string                 `json:"error_message"`
	StackTrace    string                 `json:"stack_trace"`
	Attempts      int                    `json:"attempts"`
	ConsumerGroup string                 `json:"consumer_group"`
	FailedAt      string                 `json:"failed_at"`
	Traceparent   string                 `json:"traceparent,omitempty"`
}

// PaperDiscoveredPayload payload for papers.discovered
type PaperDiscoveredPayload struct {
	ArxivID     string   `json:"arxiv_id"`
	Version     int      `json:"version"`
	Title       string   `json:"title"`
	Abstract    string   `json:"abstract"`
	Authors     []string `json:"authors"`
	Categories  []string `json:"categories"`
	PublishedAt string   `json:"published_at"`
	SourceURL   string   `json:"source_url"`
}

// PaperDownloadedPayload payload for papers.downloaded
type PaperDownloadedPayload struct {
	ArxivID        string `json:"arxiv_id"`
	Version        int    `json:"version"`
	PDFStorageKey  string `json:"pdf_storage_key"`
	SHA256         string `json:"sha256"`
	Bytes          int64  `json:"bytes"`
	StorageBucket  string `json:"storage_bucket"`
}

// PaperChunkedPayload payload for papers.chunked
type PaperChunkedPayload struct {
	PaperDBID  int64  `json:"paper_db_id"`
	ArxivID    string `json:"arxiv_id"`
	Version    int    `json:"version"`
	ChunkCount int    `json:"chunk_count"`
}
