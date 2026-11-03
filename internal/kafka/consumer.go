package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"arxivlens/shared/events"
	"github.com/twmb/franz-go/pkg/kgo"
)

type RecordHandler func(ctx context.Context, record *kgo.Record) error

// consumerClient is the part of *kgo.Client the loop uses; dlqPublisher is satisfied by *Producer.
type consumerClient interface {
	PollFetches(ctx context.Context) kgo.Fetches
	CommitRecords(ctx context.Context, rs ...*kgo.Record) error
	AllowRebalance()
	Close()
}

type dlqPublisher interface {
	PublishDLQ(ctx context.Context, dlqTopic string, dlqPayload events.DLQPayload) error
}

var errNoDLQPublisher = errors.New("no DLQ publisher configured")

type Consumer struct {
	client     consumerClient
	group      string
	topics     []string
	dlq        dlqPublisher
	maxRetries int
	backoff    func(attempt int) time.Duration
	dlqBackoff func(round int) time.Duration
	wait       func(ctx context.Context, d time.Duration) error // ctxWait
}

func NewConsumer(brokers []string, group string, topics []string, producer *Producer) (*Consumer, error) {
	if producer == nil { // checked before wrapping in the interface, so a typed nil can't slip through
		return nil, errors.New("kafka consumer needs a DLQ producer: a failed record must reach the DLQ before its offset is committed")
	}

	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topics...),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(), // released by AllowRebalance after each poll in Start
	}

	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create kafka consumer client: %w", err)
	}

	return &Consumer{
		client:     client,
		group:      group,
		topics:     topics,
		dlq:        producer,
		maxRetries: 3,
		backoff:    retryBackoff,
		dlqBackoff: dlqRetryBackoff,
		wait:       ctxWait,
	}, nil
}

func (c *Consumer) Close() {
	if c.client != nil {
		c.client.Close()
	}
}

// retryBackoff returns the in-process retry delay after a failed attempt: 100ms × 2^attempt.
func retryBackoff(attempt int) time.Duration {
	return time.Duration(100*(1<<attempt)) * time.Millisecond
}

// dlqRetryBackoff returns the wait after failed DLQ write round n (n >= 1): 1s, 2s, 4s, … capped at 30s.
func dlqRetryBackoff(round int) time.Duration {
	return min(time.Second<<min(round-1, 5), 30*time.Second)
}

// ctxWait waits for d and returns ctx.Err() if ctx ends first.
func ctxWait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Start consuming loop. Returns nil when ctx ends, kgo.ErrClientClosed if the client is closed.
func (c *Consumer) Start(ctx context.Context, handler RecordHandler) error {
	log.Printf("[Kafka Consumer] Starting group=%s topics=%v", c.group, c.topics)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[Kafka Consumer] Context cancelled, shutting down group=%s", c.group)
			return nil
		default:
		}

		// Partition errors come next to other partitions' records: log them, then still handle those records.
		fetches := c.client.PollFetches(ctx)
		closed := false
		fetches.EachError(func(topic string, partition int32, err error) {
			closed = closed || errors.Is(err, kgo.ErrClientClosed)
			log.Printf("[Kafka Consumer] Poll error (topic=%s partition=%d): %v", topic, partition, err)
		})
		if closed {
			return kgo.ErrClientClosed
		}

		// Records are resolved in order. If ctx ends before a record is handled or confirmed in the DLQ,
		// `break records` skips its commit and every later one, so no cumulative commit can sweep it up (§4 L4).
		iter := fetches.RecordIter()
	records:
		for !iter.Done() {
			record := iter.Next()

			// Extract traceparent header if present
			var traceparent string
			for _, h := range record.Headers {
				if h.Key == "traceparent" {
					traceparent = string(h.Value)
				}
			}

			// Process with bounded in-process retries (§6 Tier 1)
			var handleErr error
			for attempt := 1; attempt <= c.maxRetries; attempt++ {
				handleErr = handler(ctx, record)
				if handleErr == nil {
					break
				}
				if ctx.Err() != nil {
					break records // shutdown is not a processing failure: no DLQ write, no commit
				}

				log.Printf("[Kafka Consumer] Error processing record (topic=%s, partition=%d, offset=%d, attempt=%d/%d): %v",
					record.Topic, record.Partition, record.Offset, attempt, c.maxRetries, handleErr)

				if attempt < c.maxRetries && c.wait(ctx, c.backoff(attempt)) != nil {
					break records
				}
			}

			// If still failing after maxRetries -> route to DLQ (§6 Tier 2). The handler is not re-run; the DLQ
			// write is retried in place until it lands or ctx ends. The payload is built once (one FailedAt).
			if handleErr != nil {
				dlqTopic := fmt.Sprintf("%s.dlq", record.Topic)
				log.Printf("[Kafka Consumer] Routing failed record to DLQ topic=%s (key=%s)", dlqTopic, string(record.Key))

				var originalVal map[string]interface{}
				_ = json.Unmarshal(record.Value, &originalVal)

				dlqPayload := events.DLQPayload{
					OriginalTopic: record.Topic,
					Partition:     record.Partition,
					Offset:        record.Offset,
					OriginalKey:   string(record.Key),
					OriginalValue: originalVal,
					ErrorType:     "ProcessingError",
					ErrorMessage:  handleErr.Error(),
					StackTrace:    fmt.Sprintf("%+v", handleErr),
					Attempts:      c.maxRetries,
					ConsumerGroup: c.group,
					FailedAt:      time.Now().UTC().Format(time.RFC3339Nano),
					Traceparent:   traceparent,
				}

				for round := 1; ; round++ {
					dlqErr := errNoDLQPublisher
					if c.dlq != nil {
						dlqErr = c.dlq.PublishDLQ(ctx, dlqTopic, dlqPayload)
					}
					if dlqErr == nil {
						break
					}
					log.Printf("[Kafka Consumer] CRITICAL: DLQ write failed (topic=%s partition=%d offset=%d dlq_topic=%s round=%d): %v",
						record.Topic, record.Partition, record.Offset, dlqTopic, round, dlqErr)
					if c.wait(ctx, c.dlqBackoff(round)) != nil {
						break records
					}
				}
			}

			// Commit offset only after processing (or DLQ routing) completes (§4 Layer 4)
			if err := c.client.CommitRecords(ctx, record); err != nil {
				log.Printf("[Kafka Consumer] Offset commit error: %v", err)
			}
		}
		c.client.AllowRebalance() // after every poll, so revoking rebalances aren't blocked forever
	}
}
