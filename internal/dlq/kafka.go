package dlq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"arxivlens/shared/events"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// Watermark is one partition's offsets at a point in time. High is the next offset to be written (exclusive).
type Watermark struct{ Low, High int64 }

// Record is one DLQ record, decoded or not. Payload is nil when Value is not a valid DLQPayload; the record is never dropped.
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
	// A poll error or a stall is returned as an error, never as "end".
	Read(ctx context.Context, topic string, p int32, from, until int64, fn func(Record) bool) error
}

// ErrPartitionStalled: a partition made no progress for StallTimeout while its position was below until (wrapped, errors.Is).
var ErrPartitionStalled = errors.New("dlq: partition stalled")

// KafkaReader is the franz-go Reader. Change the exported fields before the first call.
type KafkaReader struct {
	brokers       []string
	admin         *kgo.Client   // Metadata + ListOffsets only; it consumes nothing
	FetchMaxBytes int32         // per fetch and per partition; 0 keeps the franz-go defaults
	PollTimeout   time.Duration // one PollFetches; an empty poll is not "end"
	StallTimeout  time.Duration // no progress for this long while below until -> ErrPartitionStalled
	polls         atomic.Int64
}

// NewKafkaReader connects lazily (PollTimeout 1 s, StallTimeout 10 s); call Close when done.
func NewKafkaReader(brokers []string) (*KafkaReader, error) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		return nil, fmt.Errorf("dlq: reader client: %w", err)
	}
	return &KafkaReader{brokers: brokers, admin: cl, PollTimeout: time.Second, StallTimeout: 10 * time.Second}, nil
}

func (r *KafkaReader) Close() { r.admin.Close() }

// Polls is the number of polls, across all Read calls, that returned at least one record.
func (r *KafkaReader) Polls() int64 { return r.polls.Load() }

// Watermarks: one Metadata request, then ListOffsets at -2 (log start) and -1 (high watermark), sharded to the leaders by franz-go.
func (r *KafkaReader) Watermarks(ctx context.Context, topic string) (map[int32]Watermark, error) {
	meta := kmsg.NewPtrMetadataRequest()
	mt := kmsg.NewMetadataRequestTopic()
	mt.Topic = kmsg.StringPtr(topic)
	meta.Topics = []kmsg.MetadataRequestTopic{mt}
	meta.AllowAutoTopicCreation = false
	mresp, err := meta.RequestWith(ctx, r.admin)
	if err != nil {
		return nil, fmt.Errorf("dlq: metadata for %s: %w", topic, err)
	}
	parts, err := partitionsFromMetadata(topic, mresp)
	if err != nil {
		return nil, err
	}
	var offs [2]map[int32]int64 // log start, high watermark
	for i, ts := range []int64{-2, -1} {
		if offs[i], err = r.listOffsets(ctx, topic, parts, ts); err != nil {
			return nil, err
		}
	}
	return mergeWatermarks(parts, offs[0], offs[1])
}

func (r *KafkaReader) listOffsets(ctx context.Context, topic string, parts []int32, ts int64) (map[int32]int64, error) {
	req := kmsg.NewPtrListOffsetsRequest()
	req.ReplicaID = -1
	rt := kmsg.NewListOffsetsRequestTopic()
	rt.Topic = topic
	for _, p := range parts {
		rp := kmsg.NewListOffsetsRequestTopicPartition()
		rp.Partition, rp.Timestamp, rp.CurrentLeaderEpoch = p, ts, -1
		rt.Partitions = append(rt.Partitions, rp)
	}
	req.Topics = []kmsg.ListOffsetsRequestTopic{rt}
	resp, err := req.RequestWith(ctx, r.admin)
	if err != nil {
		return nil, fmt.Errorf("dlq: list offsets (%d) for %s: %w", ts, topic, err)
	}
	return offsetsFromResponse(topic, resp)
}

// partitionsFromMetadata: an unknown topic is an error wrapping kerr.UnknownTopicOrPartition.
func partitionsFromMetadata(topic string, resp *kmsg.MetadataResponse) ([]int32, error) {
	if len(resp.Topics) != 1 {
		return nil, fmt.Errorf("dlq: metadata for %s: %d topics in the response, want 1", topic, len(resp.Topics))
	}
	t := resp.Topics[0]
	if err := kerr.ErrorForCode(t.ErrorCode); err != nil {
		return nil, fmt.Errorf("dlq: metadata for %s: %w", topic, err)
	}
	parts := make([]int32, 0, len(t.Partitions))
	for _, p := range t.Partitions {
		parts = append(parts, p.Partition)
	}
	return parts, nil
}

func offsetsFromResponse(topic string, resp *kmsg.ListOffsetsResponse) (map[int32]int64, error) {
	out := map[int32]int64{}
	for _, t := range resp.Topics { // the request names one topic
		for _, p := range t.Partitions {
			if err := kerr.ErrorForCode(p.ErrorCode); err != nil {
				return nil, fmt.Errorf("dlq: list offsets %s/%d: %w", topic, p.Partition, err)
			}
			out[p.Partition] = p.Offset
		}
	}
	return out, nil
}

// mergeWatermarks pairs log-start and high offsets; a partition missing from either answer is an error.
func mergeWatermarks(parts []int32, low, high map[int32]int64) (map[int32]Watermark, error) {
	out := make(map[int32]Watermark, len(parts))
	for _, p := range parts {
		l, okL := low[p]
		h, okH := high[p]
		if !okL || !okH || l > h {
			return nil, fmt.Errorf("dlq: partition %d: no valid watermarks (log start %d ok=%v, high %d ok=%v)", p, l, okL, h, okH)
		}
		out[p] = Watermark{Low: l, High: h}
	}
	return out, nil
}

// Read consumes partition p from offset from with ConsumePartitions: no group, nothing committed.
func (r *KafkaReader) Read(ctx context.Context, topic string, p int32, from, until int64, fn func(Record) bool) error {
	if from >= until {
		return nil
	}
	opts := []kgo.Opt{kgo.SeedBrokers(r.brokers...), kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: {p: kgo.NewOffset().At(from)}})}
	if r.FetchMaxBytes > 0 {
		opts = append(opts, kgo.FetchMaxBytes(r.FetchMaxBytes), kgo.FetchMaxPartitionBytes(r.FetchMaxBytes))
	}
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		return fmt.Errorf("dlq: read client: %w", err)
	}
	defer cl.Close()
	return r.readLoop(ctx, cl.PollFetches, topic, p, from, until, fn)
}

// readLoop polls (cl.PollFetches; a fake in tests) until offset until-1; records below the position or at/after until are ignored.
func (r *KafkaReader) readLoop(ctx context.Context, poll func(context.Context) kgo.Fetches, topic string, p int32, from, until int64, fn func(Record) bool) error {
	next, progressAt := from, time.Now()
	for next < until {
		pollCtx, cancel := context.WithTimeout(ctx, r.PollTimeout)
		fs := poll(pollCtx)
		cancel()
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, fe := range fs.Errors() { // the per-poll deadline comes back as a context error: that is an empty poll
			if !errors.Is(fe.Err, context.DeadlineExceeded) && !errors.Is(fe.Err, context.Canceled) {
				return fmt.Errorf("dlq: poll %s/%d at offset %d: %w", topic, p, next, fe.Err)
			}
		}
		if fs.NumRecords() > 0 {
			r.polls.Add(1)
		}
		stop := false
		fs.EachRecord(func(rec *kgo.Record) {
			if stop || rec.Partition != p || rec.Offset < next || rec.Offset >= until {
				return
			}
			next, progressAt = rec.Offset+1, time.Now()
			stop = !fn(decodeRecord(rec))
		})
		if stop {
			return nil
		}
		if next < until && time.Since(progressAt) >= r.StallTimeout {
			return fmt.Errorf("%w: %s/%d at offset %d below the start-time high watermark %d (no record for %s)",
				ErrPartitionStalled, topic, p, next, until, r.StallTimeout)
		}
	}
	return nil
}

// decodeRecord keeps every record. Payload is set only for a JSON object that decodes as a DLQPayload with an
// original_topic or error_type (every DLQ writer sets both), so null, {} or garbage stay visible as undecodable.
func decodeRecord(rec *kgo.Record) Record {
	out := Record{Partition: rec.Partition, Offset: rec.Offset, Timestamp: rec.Timestamp, Value: rec.Value}
	var pl events.DLQPayload
	if json.Unmarshal(rec.Value, &pl) == nil && (pl.OriginalTopic != "" || pl.ErrorType != "") {
		out.Payload = &pl
	}
	return out
}
