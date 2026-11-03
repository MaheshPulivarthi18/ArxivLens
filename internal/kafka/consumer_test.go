package kafka

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"testing"
	"time"

	"arxivlens/shared/events"
	"github.com/twmb/franz-go/pkg/kgo"
)

func TestRetryBackoff(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 200 * time.Millisecond},
		{attempt: 2, want: 400 * time.Millisecond},
	}

	for _, tt := range tests {
		if got := retryBackoff(tt.attempt); got != tt.want {
			t.Errorf("retryBackoff(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

const topic, ms, sec = "papers.discovered", time.Millisecond, time.Second

var errBoom, fail05 = errors.New("boom"), map[string]int{"0@5": -1}

// fake is the client, DLQ publisher, wait function and handler; it logs poll/commit/allow/dlq calls in order.
// It cancels ctx when its scripted polls run out (a shutdown) and on wait number cancelAt.
type fake struct {
	log, topics  []string
	polls        []kgo.Fetches
	ctx          context.Context
	cancel       context.CancelFunc
	commitErr    error
	dlqErrs      []error // error of PublishDLQ call i, then dlqErr
	dlqErr       error
	payloads     []events.DLQPayload
	waits        []time.Duration
	cancelAt     int
	fails, calls map[string]int // handler fails the first fails["p@o"] calls (-1 = always)
}

func (f *fake) add(format string, a ...any) { f.log = append(f.log, fmt.Sprintf(format, a...)) }
func (f *fake) AllowRebalance()             { f.add("allow") }
func (f *fake) Close()                      {}

func (f *fake) PollFetches(ctx context.Context) kgo.Fetches {
	if len(f.polls) == 0 {
		f.add("poll(end)")
		f.cancel()
		return fetchOf(kgo.FetchPartition{Partition: -1, Err: ctx.Err()})
	}
	f.add("poll")
	p := f.polls[0]
	f.polls = f.polls[1:]
	return p
}

func (f *fake) CommitRecords(_ context.Context, rs ...*kgo.Record) error {
	for _, r := range rs {
		f.add("commit %d@%d", r.Partition, r.Offset)
	}
	return f.commitErr
}

func (f *fake) PublishDLQ(_ context.Context, dlqTopic string, p events.DLQPayload) error {
	err := f.dlqErr
	if n := len(f.payloads); n < len(f.dlqErrs) {
		err = f.dlqErrs[n]
	}
	f.topics, f.payloads = append(f.topics, dlqTopic), append(f.payloads, p)
	f.add("dlq %d@%d err=%v", p.Partition, p.Offset, err)
	return err
}

func (f *fake) wait(ctx context.Context, d time.Duration) error {
	if f.waits = append(f.waits, d); len(f.waits) == f.cancelAt {
		f.cancel()
	}
	return ctx.Err()
}

func (f *fake) handle(_ context.Context, r *kgo.Record) error {
	k := fmt.Sprintf("%d@%d", r.Partition, r.Offset)
	if f.calls[k]++; f.fails[k] < 0 || f.calls[k] <= f.fails[k] {
		return errBoom
	}
	return nil
}

func (f *fake) grep(prefix string) (out []string) {
	for _, e := range f.log {
		if strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

func (f *fake) run(t *testing.T, c *Consumer) {
	if err := c.Start(f.ctx, f.handle); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
}

func part(p int32, offsets ...int64) kgo.FetchPartition {
	fp := kgo.FetchPartition{Partition: p}
	for _, o := range offsets {
		fp.Records = append(fp.Records, &kgo.Record{Topic: topic, Partition: p, Offset: o, Value: []byte(`{"v":"SENTINEL-VALUE-7f3a"}`)})
	}
	return fp
}

func fetchOf(parts ...kgo.FetchPartition) kgo.Fetches {
	return kgo.Fetches{{Topics: []kgo.FetchTopic{{Topic: topic, Partitions: parts}}}}
}

func newFake(t *testing.T, fails map[string]int, polls ...kgo.Fetches) (*fake, *Consumer) {
	f := &fake{polls: polls, fails: fails, calls: map[string]int{}}
	f.ctx, f.cancel = context.WithCancel(context.Background())
	t.Cleanup(f.cancel)
	return f, &Consumer{client: f, dlq: f, group: "downloader-v1", topics: []string{topic}, maxRetries: 3,
		backoff: retryBackoff, dlqBackoff: dlqRetryBackoff, wait: f.wait}
}

func TestDLQRetryBackoff(t *testing.T) {
	for i, want := range []time.Duration{1 * sec, 2 * sec, 4 * sec, 8 * sec, 16 * sec, 30 * sec, 30 * sec} {
		if got := dlqRetryBackoff(i + 1); got != want {
			t.Errorf("dlqRetryBackoff(%d) = %v, want %v", i+1, got, want)
		}
	}
}

func TestCtxWait(t *testing.T) {
	for d, wantErr := range map[time.Duration]error{ms: nil, 10 * sec: context.Canceled} {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(5*ms, cancel)
		start := time.Now()
		if err := ctxWait(ctx, d); !errors.Is(err, wantErr) || time.Since(start) > 50*ms {
			t.Errorf("ctxWait(%v) = %v after %v, want %v within 50ms", d, err, time.Since(start), wantErr)
		}
		cancel()
	}
}

// TestRetrySleepSchedule drives the real loop and pins the handler-retry waits for maxRetries = 3 (AC7).
func TestRetrySleepSchedule(t *testing.T) {
	f, c := newFake(t, fail05, fetchOf(part(0, 5)))
	if f.run(t, c); !slices.Equal(f.waits, []time.Duration{200 * ms, 400 * ms}) {
		t.Fatalf("waits = %v, want [200ms 400ms]", f.waits)
	}
}

func TestConsumerSuccessPath(t *testing.T) {
	for _, tt := range []struct {
		name      string
		fails     int
		commitErr error
		wantWaits []time.Duration
	}{
		{"ok on 1st", 0, nil, nil},
		{"ok after 1 failure", 1, nil, []time.Duration{200 * ms}},
		{"ok after 2 failures", 2, nil, []time.Duration{200 * ms, 400 * ms}},
		{"commit error is logged, loop continues", 0, errBoom, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, c := newFake(t, map[string]int{"0@5": tt.fails}, fetchOf(part(0, 5)), fetchOf(part(0, 6)))
			f.commitErr = tt.commitErr
			f.run(t, c)
			if want := []string{"commit 0@5", "commit 0@6"}; !slices.Equal(f.grep("commit"), want) || len(f.payloads) != 0 || !slices.Equal(f.waits, tt.wantWaits) {
				t.Errorf("log = %v, waits = %v; want commits %v, no DLQ, waits %v", f.log, f.waits, want, tt.wantWaits)
			}
		})
	}
}

func TestConsumerDLQThenCommit(t *testing.T) {
	fp := part(0, 5)
	fp.Records[0].Headers = []kgo.RecordHeader{{Key: "traceparent", Value: []byte("00-abc-def-01")}}
	f, c := newFake(t, fail05, fetchOf(fp))
	f.run(t, c)
	if len(f.payloads) != 1 || f.topics[0] != topic+".dlq" {
		t.Fatalf("DLQ calls to %v, want exactly 1 to %s.dlq", f.topics, topic)
	}
	if p := f.payloads[0]; p.OriginalTopic != topic || p.Partition != 0 || p.Offset != 5 || p.Attempts != 3 ||
		p.ConsumerGroup != "downloader-v1" || p.Traceparent != "00-abc-def-01" || p.ErrorMessage != "boom" {
		t.Errorf("payload = %+v", p)
	}
	if want := []string{"poll", "dlq 0@5 err=<nil>", "commit 0@5", "allow"}; !slices.Equal(f.log[:min(4, len(f.log))], want) {
		t.Errorf("log = %v, want prefix %v (publish before commit)", f.log, want)
	}
}

func TestConsumerDLQFailureBlocksPartition(t *testing.T) {
	for name, tt := range map[string]struct {
		offsets []int64
		commits []string
	}{
		"failing record first":                    {[]int64{5, 6}, nil},
		"earlier record in the poll is committed": {[]int64{4, 5, 6}, []string{"commit 0@4"}},
	} {
		t.Run(name, func(t *testing.T) {
			f, c := newFake(t, fail05, fetchOf(part(0, tt.offsets...)))
			f.dlqErr, f.cancelAt = errBoom, 5 // 2 handler waits, then cancel in the 3rd DLQ round's wait
			f.run(t, c)
			if got := f.grep("commit"); !slices.Equal(got, tt.commits) {
				t.Errorf("commits = %v, want %v: nothing at or after 0@5 while it is unresolved", got, tt.commits)
			}
			if len(f.grep("dlq 0@5")) < 2 || f.calls["0@5"] != 3 || f.calls["0@6"] != 0 {
				t.Errorf("DLQ writes %v (want >= 2), handler calls %v (want 0@5:3, no 0@6)", f.grep("dlq"), f.calls)
			}
			if want := []time.Duration{200 * ms, 400 * ms, 1 * sec, 2 * sec, 4 * sec}; !slices.Equal(f.waits, want) {
				t.Errorf("waits = %v, want %v", f.waits, want)
			}
		})
	}
}

func TestConsumerDLQRecovers(t *testing.T) {
	f, c := newFake(t, fail05, fetchOf(part(0, 5)))
	f.dlqErrs = []error{errBoom, errBoom}
	f.run(t, c)
	if want := []string{"poll", "dlq 0@5 err=boom", "dlq 0@5 err=boom", "dlq 0@5 err=<nil>", "commit 0@5", "allow"}; !slices.Equal(f.log[:min(6, len(f.log))], want) || len(f.grep("commit")) != 1 {
		t.Errorf("log = %v, want prefix %v and one commit", f.log, want)
	}
	if want := []time.Duration{200 * ms, 400 * ms, 1 * sec, 2 * sec}; f.calls["0@5"] != 3 || !slices.Equal(f.waits, want) {
		t.Errorf("handler calls = %d (want 3), waits = %v (want %v)", f.calls["0@5"], f.waits, want)
	}
	if len(f.payloads) != 3 || f.payloads[0].FailedAt != f.payloads[2].FailedAt {
		t.Errorf("want 3 DLQ writes with one FailedAt, got %+v", f.payloads)
	}
}

func TestNewConsumerRejectsNilProducer(t *testing.T) {
	if c, err := NewConsumer([]string{"localhost:9092"}, "g", []string{topic}, nil); err == nil || c != nil {
		t.Fatalf("NewConsumer(nil producer) = %v, %v; want nil, error", c, err)
	}
}

func TestConsumerNilDLQIsFailedPublish(t *testing.T) {
	f, c := newFake(t, fail05, fetchOf(part(0, 5)))
	c.dlq, f.cancelAt = nil, 3
	if f.run(t, c); f.grep("commit") != nil || !slices.Equal(f.waits, []time.Duration{200 * ms, 400 * ms, 1 * sec}) {
		t.Errorf("log = %v, waits = %v; want no commit and a DLQ retry wait", f.log, f.waits)
	}
}

func TestConsumerCancelDuringBackoffReturnsPromptly(t *testing.T) {
	f, c := newFake(t, fail05, fetchOf(part(0, 5)))
	c.wait, c.backoff = ctxWait, func(int) time.Duration { return 5 * sec }
	time.AfterFunc(20*ms, f.cancel)
	start := time.Now()
	if f.run(t, c); time.Since(start) > 200*ms || f.grep("commit") != nil || f.payloads != nil {
		t.Errorf("Start returned after %v with log %v; want < 200ms, no commit, no DLQ", time.Since(start), f.log)
	}
}

// AC6b: ctx cancelled while the handler runs its last attempt → no DLQ write, no commit (fake.PublishDLQ ignores ctx).
func TestConsumerCancelInsideHandlerNoDLQ(t *testing.T) {
	f, c := newFake(t, nil, fetchOf(part(0, 5)))
	calls := 0
	err := c.Start(f.ctx, func(context.Context, *kgo.Record) error {
		if calls++; calls == 3 {
			f.cancel()
		}
		return errBoom
	})
	if err != nil || calls != 3 || f.payloads != nil || f.grep("commit") != nil {
		t.Errorf("Start = %v, handler calls = %d, log = %v; want nil, 3, no DLQ write, no commit", err, calls, f.log)
	}
}

func TestConsumerAllowRebalanceAfterEachPoll(t *testing.T) {
	f, c := newFake(t, nil, fetchOf(part(0, 1), part(1, 1)), fetchOf(part(0, 2), part(1, 2)),
		fetchOf(kgo.FetchPartition{Partition: 1, Err: errBoom}))
	want := []string{"poll", "commit 0@1", "commit 1@1", "allow", "poll", "commit 0@2", "commit 1@2", "allow",
		"poll", "allow", "poll(end)", "allow"}
	if f.run(t, c); !slices.Equal(f.log, want) {
		t.Errorf("log = %v, want %v", f.log, want)
	}
}

func TestConsumerDLQFailureLogNoValue(t *testing.T) {
	var buf bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&buf)
	f, c := newFake(t, fail05, fetchOf(part(0, 5)))
	f.dlqErrs = []error{errors.New("broker down")}
	f.run(t, c)
	want := "CRITICAL: DLQ write failed (topic=" + topic + " partition=0 offset=5 dlq_topic=" + topic + ".dlq round=1): broker down"
	if out := buf.String(); !strings.Contains(out, want) || strings.Contains(out, "SENTINEL-VALUE-7f3a") {
		t.Errorf("want %q and no record value in the log:\n%s", want, out)
	}
}

func TestConsumerPollErrorStillProcessesRecords(t *testing.T) {
	for name, tt := range map[string]struct {
		fetch   kgo.Fetches
		wantErr error
		wantLog []string
	}{
		"partition error, other partition handled": {fetchOf(kgo.FetchPartition{Partition: 0, Err: errBoom}, part(1, 1, 2)),
			nil, []string{"poll", "commit 1@1", "commit 1@2", "allow", "poll(end)", "allow"}},
		"client closed returns the error": {fetchOf(kgo.FetchPartition{Partition: -1, Err: kgo.ErrClientClosed}), kgo.ErrClientClosed, []string{"poll"}},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			defer log.SetOutput(log.Writer())
			log.SetOutput(&buf)
			f, c := newFake(t, nil, tt.fetch)
			if err := c.Start(f.ctx, f.handle); !errors.Is(err, tt.wantErr) || !slices.Equal(f.log, tt.wantLog) ||
				!strings.Contains(buf.String(), "Poll error (topic="+topic+" partition=") {
				t.Errorf("Start = %v, log %v; want %v, %v and a poll error logged with topic and partition:\n%s", err, f.log, tt.wantErr, tt.wantLog, buf.String())
			}
		})
	}
}
