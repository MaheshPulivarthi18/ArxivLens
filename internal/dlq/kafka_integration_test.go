//go:build integration

// DLQ reader on real Kafka, own fresh topic (OPS-H7a1): KAFKA_BROKERS=localhost:9092 go test -tags integration -v ./internal/dlq/
package dlq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"arxivlens/shared/events"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

func TestReadStopsAtSnapshot_Integration(t *testing.T) {
	if os.Getenv("KAFKA_BROKERS") == "" {
		t.Skip("KAFKA_BROKERS not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	seeds, topic := strings.Split(os.Getenv("KAFKA_BROKERS"), ","), fmt.Sprintf("it.dlq.read.%d", time.Now().UnixNano())
	cl, err := kgo.NewClient(kgo.SeedBrokers(seeds...), kgo.RecordPartitioner(kgo.ManualPartitioner()), kgo.ProducerBatchCompression(kgo.NoCompression()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	req, rt := kmsg.NewPtrCreateTopicsRequest(), kmsg.NewCreateTopicsRequestTopic()
	rt.Topic, rt.NumPartitions, rt.ReplicationFactor = topic, 3, 1
	req.Topics, req.TimeoutMillis = []kmsg.CreateTopicsRequestTopic{rt}, 30000
	if resp, err := req.RequestWith(ctx, cl); err != nil || len(resp.Topics) != 1 || kerr.ErrorForCode(resp.Topics[0].ErrorCode) != nil {
		t.Fatalf("create topic %s: %v %+v", topic, err, resp)
	}
	r, err := NewKafkaReader(seeds)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.FetchMaxBytes = 4096

	// Each ProduceSync of 25 records (~9 KB, uncompressed) is one batch, so a 4 KiB fetch returns about one batch per poll.
	produce := func(p int32, n int, tag string) {
		for i := 0; i < n; i += 25 {
			var recs []*kgo.Record
			for j := i; j < i+25 && j < n; j++ {
				key := fmt.Sprintf("%s-%d-%04d", tag, p, j)
				v, _ := json.Marshal(events.DLQPayload{OriginalTopic: "papers.downloaded", OriginalKey: key,
					ErrorType: "PdfParseError", ErrorMessage: key + strings.Repeat(" padding", 30)})
				if tag == "snap" && p == 1 && j == 250 {
					v = []byte("not a DLQPayload")
				}
				recs = append(recs, &kgo.Record{Topic: topic, Partition: p, Value: v})
			}
			if err := cl.ProduceSync(ctx, recs...).FirstErr(); err != nil {
				t.Fatalf("produce to %d: %v", p, err)
			}
		}
	}
	produce(0, 1000, "snap")
	produce(1, 500, "snap") // partition 2 stays empty
	wm, err := r.Watermarks(ctx, topic)
	if err != nil || len(wm) != 3 || wm[0].High-wm[0].Low != 1000 || wm[1].High-wm[1].Low != 500 || wm[2].Low != wm[2].High {
		t.Fatalf("watermarks %v (%v), want 1000 / 500 / empty", wm, err)
	}
	produce(0, 10, "late") // written after the snapshot: never returned
	produce(2, 5, "late")

	seen, total, undecodable := map[string]bool{}, 0, 0
	for p := int32(0); p < 3; p++ {
		w, last := wm[p], int64(-1)
		if err := r.Read(ctx, topic, p, w.Low, w.High, func(rec Record) bool {
			total++
			if rec.Partition != p || rec.Offset < w.Low || rec.Offset >= w.High || rec.Offset <= last {
				t.Errorf("record %d/%d after %d: outside [%d, %d) or out of order", rec.Partition, rec.Offset, last, w.Low, w.High)
			}
			last = rec.Offset
			if rec.Payload == nil {
				undecodable++
				if p != 1 || string(rec.Value) != "not a DLQPayload" {
					t.Errorf("undecodable record %d/%d = %q", rec.Partition, rec.Offset, rec.Value)
				}
			} else if k := rec.Payload.OriginalKey; seen[k] || !strings.HasPrefix(k, fmt.Sprintf("snap-%d-", p)) {
				t.Errorf("record %d/%d key %s: duplicate, wrong partition or written after the snapshot", rec.Partition, rec.Offset, k)
			} else {
				seen[k] = true
			}
			return true
		}); err != nil {
			t.Fatalf("read partition %d: %v", p, err)
		}
	}
	t.Logf("TestReadStopsAtSnapshot: read %d records (%d undecodable) from 3 partitions, watermarks %v, in %d polls (FetchMaxBytes=%d); 15 records written after the snapshot were not returned",
		total, undecodable, wm, r.Polls(), r.FetchMaxBytes)
	if total != 1500 || len(seen) != 1499 || undecodable != 1 || r.Polls() < 5 {
		t.Errorf("got %d records, %d distinct keys, %d undecodable, %d polls; want 1500, 1499, 1, >= 5", total, len(seen), undecodable, r.Polls())
	}

	r.StallTimeout = 3 * time.Second // until past the high watermark: a stall error, not "end"
	err = r.Read(ctx, topic, 1, wm[1].High, wm[1].High+1, func(Record) bool { return true })
	if !errors.Is(err, ErrPartitionStalled) {
		t.Errorf("err = %v, want ErrPartitionStalled", err)
	}
}
