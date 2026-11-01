package kafka

import (
	"context"
	"encoding/json"
	"fmt"

	"arxivlens/shared/events"
	"github.com/twmb/franz-go/pkg/kgo"
)

type Producer struct {
	client *kgo.Client
}

func NewProducer(brokers []string) (*Producer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.AllowAutoTopicCreation(),
		kgo.ProducerBatchMaxBytes(1024*1024),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create kafka producer client: %w", err)
	}

	return &Producer{client: client}, nil
}

func (p *Producer) Close() {
	if p.client != nil {
		p.client.Close()
	}
}

// Publish sends an EventEnvelope to the specified topic, keyed by envelope.PaperID
func (p *Producer) Publish(ctx context.Context, topic string, envelope events.EventEnvelope) error {
	valBytes, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("failed to marshal event envelope: %w", err)
	}

	record := &kgo.Record{
		Topic: topic,
		Key:   []byte(envelope.PaperID),
		Value: valBytes,
	}

	// Attach traceparent header if present (§10)
	if envelope.Traceparent != "" {
		record.Headers = append(record.Headers, kgo.RecordHeader{
			Key:   "traceparent",
			Value: []byte(envelope.Traceparent),
		})
	}

	results := p.client.ProduceSync(ctx, record)
	if err := results.FirstErr(); err != nil {
		return fmt.Errorf("failed to produce record to %s: %w", topic, err)
	}

	return nil
}

// PublishDLQ sends diagnostic error payload to the corresponding .dlq topic (§6)
func (p *Producer) PublishDLQ(ctx context.Context, dlqTopic string, dlqPayload events.DLQPayload) error {
	valBytes, err := json.Marshal(dlqPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal DLQ payload: %w", err)
	}

	record := &kgo.Record{
		Topic: dlqTopic,
		Key:   []byte(dlqPayload.OriginalKey),
		Value: valBytes,
	}

	if dlqPayload.Traceparent != "" {
		record.Headers = append(record.Headers, kgo.RecordHeader{
			Key:   "traceparent",
			Value: []byte(dlqPayload.Traceparent),
		})
	}

	results := p.client.ProduceSync(ctx, record)
	if err := results.FirstErr(); err != nil {
		return fmt.Errorf("failed to produce to DLQ topic %s: %w", dlqTopic, err)
	}

	return nil
}
