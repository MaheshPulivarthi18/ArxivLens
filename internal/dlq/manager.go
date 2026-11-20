package dlq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"arxivlens/internal/kafka"
	"arxivlens/shared/events"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

type Manager struct {
	brokers []string
	reader  Reader // List (dlq list / inspect); Replay still reads with ListEvents until OPS-H7b
}

// NewManager: reader may be nil for Replay-only use.
func NewManager(brokers []string, reader Reader) *Manager {
	return &Manager{brokers: brokers, reader: reader}
}

// DefaultPageSize is the page size when ListOptions.PageSize <= 0 (dlq list --page-size default).
const DefaultPageSize = 50

// Cursor is a P/O position (partition P, offset O), the form dlq list prints and --from takes. The zero value is unset.
type Cursor struct {
	Partition int32
	Offset    int64
	set       bool
}

// IsSet is false for the zero Cursor and for Page.Next at end of topic.
func (c Cursor) IsSet() bool { return c.set }

func (c Cursor) String() string { return fmt.Sprintf("%d/%d", c.Partition, c.Offset) }

// ParseCursor parses "P/O": two non-negative integers.
func ParseCursor(s string) (Cursor, error) {
	panic("skeleton")
}

// ListOptions: Partition restricts the listing to one partition. From starts at partition P offset O, then the later
// partitions from their Low. Snapshot (nil: List takes one) lets dlq list --all and inspect stop at the run's start-time watermarks.
type ListOptions struct {
	Partition *int32
	From      Cursor
	PageSize  int
	Snapshot  map[int32]Watermark
}

// Page is one page of a listing. Next is unset at end of topic. Warnings (expired offsets) go to stderr.
type Page struct {
	Records    []Record
	Next       Cursor
	Watermarks map[int32]Watermark
	Warnings   []string
}

// List reads up to PageSize records, in partition then offset order, below the start-time high watermarks (design OPS-H7 Q2, Q3).
// A stall or poll error is returned, never treated as end of topic; Page then holds the records read before it.
func (m *Manager) List(ctx context.Context, topic string, opt ListOptions) (Page, error) {
	panic("skeleton")
}

// listPartitions: the partitions List reads, in order, after the Partition and From filters.
func listPartitions(wm map[int32]Watermark, opt ListOptions) ([]int32, error) {
	panic("skeleton")
}

// nextCursor: the position after last, or the Low of the next non-empty partition in rest; unset at end of topic.
func nextCursor(wm map[int32]Watermark, rest []int32, last Record) Cursor {
	panic("skeleton")
}

// missingTopicError: AC6 message; it wraps kerr.UnknownTopicOrPartition from Watermarks.
type missingTopicError struct {
	topic string
	err   error
}

func (e *missingTopicError) Error() string {
	return fmt.Sprintf("dlq: topic %s does not exist (check --topic; compose's kafka-init creates the three DLQs)", e.topic)
}

func (e *missingTopicError) Unwrap() error { return e.err }

// ListEvents reads messages from a DLQ topic without committing offsets
func (m *Manager) ListEvents(ctx context.Context, dlqTopic string, limit int) ([]events.DLQPayload, error) {
	if limit <= 0 {
		limit = 50
	}

	client, err := kgo.NewClient(
		kgo.SeedBrokers(m.brokers...),
		kgo.ConsumeTopics(dlqTopic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create dlq client: %w", err)
	}
	defer client.Close()

	var results []events.DLQPayload
	deadline := time.Now().Add(2 * time.Second)

	for len(results) < limit && time.Now().Before(deadline) {
		pollCtx, pollCancel := context.WithTimeout(ctx, 1*time.Second)
		fetches := client.PollFetches(pollCtx)
		pollCancel()

		if fetches.IsClientClosed() {
			break
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			break
		}
		if fetches.NumRecords() == 0 {
			break
		}

		iter := fetches.RecordIter()
		for !iter.Done() && len(results) < limit {
			rec := iter.Next()
			var payload events.DLQPayload
			if err := json.Unmarshal(rec.Value, &payload); err == nil {
				results = append(results, payload)
			}
		}
	}

	return results, nil
}

// Replay republishes original values from DLQ topic back to original topic (§6)
func (m *Manager) Replay(
	ctx context.Context,
	dlqTopic string,
	errorTypeFilter string,
	limit int,
	dryRun bool,
) (int, error) {
	dlqEvents, err := m.ListEvents(ctx, dlqTopic, limit)
	if err != nil {
		return 0, err
	}

	producer, err := kafka.NewProducer(m.brokers)
	if err != nil {
		return 0, err
	}
	defer producer.Close()

	replayedCount := 0

	for _, dlqEv := range dlqEvents {
		if errorTypeFilter != "" && !strings.EqualFold(dlqEv.ErrorType, errorTypeFilter) {
			continue
		}

		origTopic := dlqEv.OriginalTopic
		if origTopic == "" {
			// Fallback: strip .dlq suffix
			origTopic = strings.TrimSuffix(dlqTopic, ".dlq")
		}

		// Same rule in dry-run and real runs, so a dry-run's count equals what a real run publishes.
		envelope, reason, ok := replayEnvelope(dlqEv, origTopic)
		if !ok {
			log.Print(skipLine(dlqEv, reason))
			continue
		}

		log.Printf("[DLQ Replay] Event key=%s (error=%s) -> Target Topic: %s (DryRun=%v)",
			dlqEv.OriginalKey, dlqEv.ErrorType, origTopic, dryRun)

		if !dryRun {
			if err := producer.Publish(ctx, origTopic, envelope); err != nil {
				return replayedCount, fmt.Errorf("failed to republish to %s: %w", origTopic, err)
			}
		}

		replayedCount++
	}

	return replayedCount, nil
}
