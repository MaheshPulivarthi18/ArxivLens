package dlq

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// fakePoller returns its scripted fetches, then acts like kgo on an idle partition: it waits out the per-poll
// deadline and returns the context error as a fetch error.
type fakePoller struct {
	script []kgo.Fetches
	delay  time.Duration // before each scripted fetch
}

func (f *fakePoller) PollFetches(ctx context.Context) kgo.Fetches {
	if fs := f.script; len(fs) > 0 {
		time.Sleep(f.delay)
		f.script = fs[1:]
		return fs[0]
	}
	<-ctx.Done()
	return part(-1, ctx.Err())
}

func part(p int32, err error, offs ...int64) kgo.Fetches {
	recs := make([]*kgo.Record, len(offs))
	for i, o := range offs {
		recs[i] = &kgo.Record{Topic: "t", Partition: p, Offset: o, Value: []byte(`{"error_type":"E"}`)}
	}
	return kgo.Fetches{{Topics: []kgo.FetchTopic{{Topic: "t", Partitions: []kgo.FetchPartition{{Partition: p, Err: err, Records: recs}}}}}}
}

func TestReadLoop(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name                   string
		from, until, wantPolls int64
		script                 []kgo.Fetches
		stopAfter              int
		delay                  time.Duration
		cancelled              bool
		want                   []int64
		wantErr                error
	}{
		{name: "chunks of 3 up to the snapshot, later records ignored", until: 5, wantPolls: 2,
			script: []kgo.Fetches{part(0, nil, 0, 1, 2), part(0, nil, 3, 4, 5), part(0, nil, 6)}, want: []int64{0, 1, 2, 3, 4}},
		{name: "below from, re-delivered and other partitions skipped", from: 2, until: 5, wantPolls: 3,
			script: []kgo.Fetches{part(0, nil, 0, 1, 2, 3), part(1, nil, 4), part(0, nil, 3, 4)}, want: []int64{2, 3, 4}},
		{name: "offset gap still ends at until", until: 5, wantPolls: 1, script: []kgo.Fetches{part(0, nil, 0, 4)}, want: []int64{0, 4}},
		{name: "fn false stops", until: 9, wantPolls: 1, stopAfter: 2, script: []kgo.Fetches{part(0, nil, 0, 1, 2)}, want: []int64{0, 1}},
		{name: "poll error is an error, not end", until: 5, wantPolls: 1,
			script: []kgo.Fetches{part(0, nil, 0), part(0, boom)}, want: []int64{0}, wantErr: boom},
		{name: "slow but steady progress is not a stall", until: 4, wantPolls: 4, delay: 40 * time.Millisecond,
			script: []kgo.Fetches{part(0, nil, 0), part(0, nil, 1), part(0, nil, 2), part(0, nil, 3)}, want: []int64{0, 1, 2, 3}},
		{name: "idle polls then stall", until: 3, wantPolls: 1, script: []kgo.Fetches{part(0, nil, 0)}, want: []int64{0}, wantErr: ErrPartitionStalled},
		{name: "caller context cancelled", until: 3, cancelled: true, wantErr: context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			r := &KafkaReader{PollTimeout: 5 * time.Millisecond, StallTimeout: 100 * time.Millisecond}
			var got []int64
			err := r.readLoop(ctx, (&fakePoller{script: tc.script, delay: tc.delay}).PollFetches, "t", 0, tc.from, tc.until, func(rec Record) bool {
				got = append(got, rec.Offset)
				return tc.stopAfter == 0 || len(got) < tc.stopAfter
			})
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil) != (err == nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) || r.Polls() != tc.wantPolls {
				t.Errorf("offsets %v polls %d, want %v polls %d", got, r.Polls(), tc.want, tc.wantPolls)
			}
			if want := "t/0 at offset 1 below the start-time high watermark 3"; tc.wantErr == ErrPartitionStalled && !strings.Contains(err.Error(), want) {
				t.Errorf("stall error %q, want it to name %q", err, want)
			}
		})
	}
}

func TestWatermarkResponses(t *testing.T) {
	unknown := []kmsg.MetadataResponseTopic{{ErrorCode: kerr.UnknownTopicOrPartition.Code}}
	for i, topics := range [][]kmsg.MetadataResponseTopic{unknown, nil, make([]kmsg.MetadataResponseTopic, 2)} {
		if _, err := partitionsFromMetadata("t", &kmsg.MetadataResponse{Topics: topics}); err == nil || (i == 0) != errors.Is(err, kerr.UnknownTopicOrPartition) {
			t.Errorf("metadata topics %+v: err = %v, want an error (UnknownTopicOrPartition for the first)", topics, err)
		}
	}
	parts, err := partitionsFromMetadata("t", &kmsg.MetadataResponse{Topics: []kmsg.MetadataResponseTopic{
		{Partitions: []kmsg.MetadataResponseTopicPartition{{Partition: 0}, {Partition: 2}, {Partition: 1}}}}})
	if err != nil || !reflect.DeepEqual(parts, []int32{0, 2, 1}) {
		t.Fatalf("partitions %v, %v", parts, err)
	}
	resp := func(code int16) *kmsg.ListOffsetsResponse {
		return &kmsg.ListOffsetsResponse{Topics: []kmsg.ListOffsetsResponseTopic{
			{Topic: "t", Partitions: []kmsg.ListOffsetsResponseTopicPartition{{Partition: 0, Offset: 4}, {Partition: 2, Offset: 7, ErrorCode: code}, {Partition: 1, Offset: 0}}},
		}}
	}
	if _, err := offsetsFromResponse("t", resp(kerr.NotLeaderForPartition.Code)); !errors.Is(err, kerr.NotLeaderForPartition) {
		t.Errorf("partition error: err = %v", err)
	}
	high, err := offsetsFromResponse("t", resp(0))
	if err != nil || !reflect.DeepEqual(high, map[int32]int64{0: 4, 1: 0, 2: 7}) {
		t.Fatalf("offsets %v, %v", high, err)
	}
	low := map[int32]int64{0: 1, 1: 0, 2: 7, 3: 0, 5: 6}
	high[4], high[5] = 9, 5
	wm, err := mergeWatermarks(parts, low, high)
	if want := map[int32]Watermark{0: {1, 4}, 1: {0, 0}, 2: {7, 7}}; err != nil || !reflect.DeepEqual(wm, want) {
		t.Errorf("watermarks %v, %v; want %v", wm, err, want)
	}
	for _, p := range []int32{3, 4, 5} { // no high, no low, low above high
		if _, err := mergeWatermarks([]int32{0, p}, low, high); err == nil {
			t.Errorf("partition %d: want an error", p)
		}
	}
}

func TestDecodeRecord(t *testing.T) {
	for value, decoded := range map[string]bool{
		`{"original_topic":"papers.downloaded","original_key":"2401.00001","error_type":"PdfParseError"}`: true,
		`{"error_type":"ProcessingError"}`: true, `{"original_topic":"papers.discovered"}`: true,
		`{}`: false, `null`: false, `[1,2]`: false, `"text"`: false, `not json`: false, ``: false, `{"original_key":"2401.00001"}`: false,
	} {
		got := decodeRecord(&kgo.Record{Partition: 3, Offset: 17, Timestamp: time.Unix(1700000000, 0), Value: []byte(value)})
		if (got.Payload != nil) != decoded || got.Partition != 3 || got.Offset != 17 || got.Timestamp.Unix() != 1700000000 || string(got.Value) != value {
			t.Errorf("decodeRecord(%q) = %+v, want decoded=%v with P/O 3/17 and the raw value", value, got, decoded)
		}
	}
}
