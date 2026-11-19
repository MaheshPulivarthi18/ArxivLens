package dlq

// SKELETON (OPS-H7a size gate). Bodies are stubs; see the projection in the commit message.

import (
	"context"
	"errors"
	"time"

	"arxivlens/shared/events"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// Watermark is one partition's offsets at a point in time. High is the next offset to be written (exclusive).
type Watermark struct{ Low, High int64 }

// Record is one DLQ record, decoded or not. Payload is nil when Value is not a valid DLQPayload.
type Record struct {
	Partition int32
	Offset    int64
	Timestamp time.Time
	Value     []byte
	Payload   *events.DLQPayload
}

// Reader reads a DLQ topic without a consumer group and without committing offsets (design OPS-H7 Q2).
type Reader interface {
	Watermarks(ctx context.Context, topic string) (map[int32]Watermark, error)
	// Read calls fn for each record of partition p with from <= offset < until, in offset order; fn returns false to stop.
	Read(ctx context.Context, topic string, p int32, from, until int64, fn func(Record) bool) error
}

// ErrPartitionStalled: a partition made no progress for StallTimeout while its position was below until.
var ErrPartitionStalled = errors.New("dlq: partition stalled")

// PartitionStalledError carries the stalled partition's position; errors.Is(err, ErrPartitionStalled) is true.
type PartitionStalledError struct{}

// KafkaReader is the franz-go Reader.
type KafkaReader struct {
	FetchMaxBytes int32
	StallTimeout  time.Duration
	PollTimeout   time.Duration
}

// NewKafkaReader [~10 lines]
func NewKafkaReader(brokers []string) (*KafkaReader, error) { panic("skeleton") }

// Watermarks: Metadata for the partition list, then ListOffsets at -2 (log start) and -1 (high watermark). [~30]
func (r *KafkaReader) Watermarks(ctx context.Context, topic string) (map[int32]Watermark, error) {
	panic("skeleton")
}

// Read: ConsumePartitions at from, no group, no commits; stop at until-1, stall guard, poll errors returned. [~45]
func (r *KafkaReader) Read(ctx context.Context, topic string, p int32, from, until int64, fn func(Record) bool) error {
	panic("skeleton")
}

// listOffsets [~20], offsetsFromResponse [~15], partitionScan accept/done/stalled [~20], decodeRecord [~12]
var _ = kgo.NewClient
var _ = kmsg.NewPtrListOffsetsRequest
