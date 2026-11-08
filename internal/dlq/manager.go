package dlq

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"arxivlens/internal/kafka"
	"arxivlens/shared/events"
	"github.com/twmb/franz-go/pkg/kgo"
)

type Manager struct {
	brokers []string
}

func NewManager(brokers []string) *Manager {
	return &Manager{brokers: brokers}
}

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
