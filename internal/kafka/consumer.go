package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"arxivlens/shared/events"
	"github.com/twmb/franz-go/pkg/kgo"
)

type RecordHandler func(ctx context.Context, record *kgo.Record) error

type Consumer struct {
	client     *kgo.Client
	group      string
	topics     []string
	producer   *Producer
	maxRetries int
}

func NewConsumer(brokers []string, group string, topics []string, producer *Producer) (*Consumer, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topics...),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
	}

	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create kafka consumer client: %w", err)
	}

	return &Consumer{
		client:     client,
		group:      group,
		topics:     topics,
		producer:   producer,
		maxRetries: 3,
	}, nil
}

func (c *Consumer) Close() {
	if c.client != nil {
		c.client.Close()
	}
}

// Start consuming loop.
func (c *Consumer) Start(ctx context.Context, handler RecordHandler) error {
	log.Printf("[Kafka Consumer] Starting group=%s topics=%v", c.group, c.topics)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[Kafka Consumer] Context cancelled, shutting down group=%s", c.group)
			return nil
		default:
		}

		fetches := c.client.PollFetches(ctx)
		if errs := fetches.Errors(); len(errs) > 0 {
			for _, err := range errs {
				log.Printf("[Kafka Consumer] Poll error: %v", err)
			}
			continue
		}

		iter := fetches.RecordIter()
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

				log.Printf("[Kafka Consumer] Error processing record (topic=%s, partition=%d, offset=%d, attempt=%d/%d): %v",
					record.Topic, record.Partition, record.Offset, attempt, c.maxRetries, handleErr)

				if attempt < c.maxRetries {
					backoff := time.Duration(100*(1<<attempt)) * time.Millisecond
					time.Sleep(backoff)
				}
			}

			// If still failing after maxRetries -> route to DLQ (§6 Tier 2)
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

				if c.producer != nil {
					if dlqErr := c.producer.PublishDLQ(ctx, dlqTopic, dlqPayload); dlqErr != nil {
						log.Printf("[Kafka Consumer] CRITICAL: Failed to write to DLQ: %v", dlqErr)
					}
				}
			}

			// Commit offset only after processing (or DLQ routing) completes (§4 Layer 4)
			if err := c.client.CommitRecords(ctx, record); err != nil {
				log.Printf("[Kafka Consumer] Offset commit error: %v", err)
			}
		}
	}
}
