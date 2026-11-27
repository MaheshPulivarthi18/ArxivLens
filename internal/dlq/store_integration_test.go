//go:build integration

// Replay state on real PostgreSQL (and Kafka for the end-to-end run), B-PIPE. Each test applies migration 000004
// itself (IF NOT EXISTS) and uses its own DLQ topic name. Run by .github/workflows/db.yml:
//
//	DATABASE_URL=postgres://... KAFKA_BROKERS=localhost:9092 go test -tags integration -v ./internal/dlq/
package dlq

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"arxivlens/internal/kafka"
	"arxivlens/shared/events"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// pgIT returns a migrated pool (skips without DATABASE_URL) and deletes topic's replay rows at cleanup.
func pgIT(t *testing.T, topic string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	sql, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000004_dlq_replay.up.sql"))
	if err == nil {
		_, err = pool.Exec(ctx, string(sql))
	}
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, tbl := range []string{"dlq_replay_log", "dlq_replay_checkpoints"} {
			_, _ = pool.Exec(ctx, "DELETE FROM "+tbl+" WHERE dlq_topic = $1", topic)
		}
		pool.Close()
	})
	return pool
}

func TestPGStore_Integration(t *testing.T) {
	topic := fmt.Sprintf("it.dlq.store.%d", time.Now().UnixNano())
	s, ctx := NewPGStore(pgIT(t, topic)), context.Background()
	steps := []Resolution{
		{DLQTopic: topic, Partition: 0, Offset: 0, Outcome: OutcomeReplayed, TargetTopic: "papers.downloaded", EventID: "e0", Next: 1},
		{DLQTopic: topic, Partition: 0, Offset: 2, Outcome: OutcomeUnreplayable}, // after a filtered record: no checkpoint move
		{DLQTopic: topic, Partition: 0, Next: 1},                                 // checkpoint-only, never backwards
		{DLQTopic: topic, Partition: 1, Next: 5},
	}
	for _, r := range steps {
		if err := s.MarkResolved(ctx, r); err != nil {
			t.Fatalf("MarkResolved(%+v): %v", r, err)
		}
	}
	if err := s.MarkResolved(ctx, Resolution{DLQTopic: topic, Partition: 0, Offset: 3, Outcome: "bogus", Next: 9}); err == nil {
		t.Error("an invalid outcome was accepted")
	}
	cps, err := s.Checkpoints(ctx, topic)
	if err != nil || !reflect.DeepEqual(cps, map[int32]int64{0: 1, 1: 5}) {
		t.Fatalf("Checkpoints = %v, %v; want {0:1 1:5} (the failed tx moved nothing)", cps, err)
	}
	got, err := s.Resolved(ctx, topic, 0, 1)
	if err != nil || !reflect.DeepEqual(got, map[int64]bool{2: true}) {
		t.Errorf("Resolved(p0, from 1) = %v, %v; want {2}", got, err)
	}
}

// AC3 on real Kafka + Postgres: 2N DLQ records over 2 partitions, --limit N twice, then the target topic holds each
// record's envelope once, in (partition, offset) order, and a third run publishes nothing.
func TestReplayResumes_Integration(t *testing.T) {
	if os.Getenv("KAFKA_BROKERS") == "" {
		t.Skip("KAFKA_BROKERS not set")
	}
	const n = 5
	stamp := time.Now().UnixNano()
	dlqT, target := fmt.Sprintf("it.dlq.replay.%d.dlq", stamp), fmt.Sprintf("it.dlq.replay.%d", stamp)
	st := NewPGStore(pgIT(t, dlqT))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	seeds := strings.Split(os.Getenv("KAFKA_BROKERS"), ",")
	cl, err := kgo.NewClient(kgo.SeedBrokers(seeds...), kgo.RecordPartitioner(kgo.ManualPartitioner()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	req := kmsg.NewPtrCreateTopicsRequest()
	for topic, parts := range map[string]int32{dlqT: 2, target: 1} { // one target partition: consume order = publish order
		rt := kmsg.NewCreateTopicsRequestTopic()
		rt.Topic, rt.NumPartitions, rt.ReplicationFactor = topic, parts, 1
		req.Topics = append(req.Topics, rt)
	}
	req.TimeoutMillis = 30000
	if resp, err := req.RequestWith(ctx, cl); err != nil || len(resp.Topics) != 2 || kerr.ErrorForCode(resp.Topics[0].ErrorCode) != nil || kerr.ErrorForCode(resp.Topics[1].ErrorCode) != nil {
		t.Fatalf("create topics: %v %+v", err, resp)
	}
	var want []string
	for i := 0; i < 2*n; i++ {
		p := int32(0)
		if i >= n+1 { // n+1 records in partition 0, n-1 in partition 1
			p = 1
		}
		id := fmt.Sprintf("it-%d-%02d", stamp, i)
		want = append(want, id)
		v, _ := json.Marshal(events.DLQPayload{OriginalTopic: target, OriginalKey: "2401.000" + fmt.Sprint(i), ErrorType: "PdfParseError",
			OriginalValue: map[string]interface{}{"event_id": id, "event_type": target, "paper_id": "2401.000" + fmt.Sprint(i), "attempt": 3}})
		if err := cl.ProduceSync(ctx, &kgo.Record{Topic: dlqT, Partition: p, Value: v}).FirstErr(); err != nil {
			t.Fatal(err)
		}
	}
	r, err := NewKafkaReader(seeds)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	pub, err := kafka.NewProducer(seeds)
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	m := NewManager(r, pub, st)
	for run, wantN := range []int{n, n, 0} {
		res, err := m.Replay(ctx, dlqT, ReplayOptions{Limit: n})
		if err != nil || res.Published != wantN {
			t.Fatalf("run %d: published %d, err %v; want %d", run+1, res.Published, err, wantN)
		}
	}
	cps, err := st.Checkpoints(ctx, dlqT)
	if err != nil || !reflect.DeepEqual(cps, map[int32]int64{0: n + 1, 1: n - 1}) {
		t.Errorf("checkpoints %v (%v), want High {0:%d 1:%d}", cps, err, n+1, n-1)
	}
	cons, err := kgo.NewClient(kgo.SeedBrokers(seeds...), kgo.ConsumeTopics(target), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer cons.Close()
	var got []string
	for deadline, extra := time.Now().Add(30*time.Second), 0; time.Now().Before(deadline) && extra < 2; {
		if len(got) >= len(want) {
			extra++ // two more polls, so a duplicate publish would show up
		}
		pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
		cons.PollFetches(pctx).EachRecord(func(rec *kgo.Record) {
			var e events.EventEnvelope
			_ = json.Unmarshal(rec.Value, &e)
			got = append(got, e.EventID)
		})
		pcancel()
	}
	t.Logf("TestReplayResumes_Integration: target topic %s holds %d envelopes: %v", target, len(got), got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("target topic event_ids = %v, want %v (each once, in partition/offset order)", got, want)
	}
}
