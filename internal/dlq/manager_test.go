package dlq

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"arxivlens/shared/events"
	"github.com/twmb/franz-go/pkg/kerr"
)

// fakeReader is an in-memory topic. Read delivers records in chunks of 3 (one chunk = one poll), honours [from, until)
// and fn's stop, and can fail after a scripted number of records. late records are appended once Watermarks has been taken.
type fakeReader struct {
	low     map[int32]int64
	log     map[int32][]Record
	late    map[int32]int // records appended to the partition right after the first Watermarks
	wmErr   error
	readErr error // returned by Read once failAt records have been delivered (-1: never)
	failAt  int
	polls   int
	wmCalls int
	reads   []string // "p:from-until"
}

func newFakeReader(parts map[int32][2]int64) *fakeReader { // partition -> {low, count}
	f := &fakeReader{low: map[int32]int64{}, log: map[int32][]Record{}, late: map[int32]int{}, failAt: -1}
	for p, lc := range parts {
		f.low[p], f.log[p] = lc[0], []Record{}
		f.add(p, int(lc[1]))
	}
	return f
}

func (f *fakeReader) add(p int32, n int) {
	for i := 0; i < n; i++ {
		o := f.low[p] + int64(len(f.log[p]))
		f.log[p] = append(f.log[p], Record{Partition: p, Offset: o, Value: []byte("{}"),
			Payload: &events.DLQPayload{OriginalKey: fmt.Sprintf("k%d-%d", p, o), ErrorType: "E"}})
	}
}

func (f *fakeReader) Watermarks(_ context.Context, _ string) (map[int32]Watermark, error) {
	f.wmCalls++
	if f.wmErr != nil {
		return nil, f.wmErr
	}
	wm := map[int32]Watermark{}
	for p, recs := range f.log {
		wm[p] = Watermark{Low: f.low[p], High: f.low[p] + int64(len(recs))}
	}
	for p, n := range f.late {
		f.add(p, n)
	}
	f.late = nil
	return wm, nil
}

func (f *fakeReader) Read(_ context.Context, _ string, p int32, from, until int64, fn func(Record) bool) error {
	f.reads = append(f.reads, fmt.Sprintf("%d:%d-%d", p, from, until))
	var in []Record
	for _, r := range f.log[p] {
		if r.Offset >= from && r.Offset < until {
			in = append(in, r)
		}
	}
	for len(in) > 0 {
		chunk := in[:min(3, len(in))]
		in = in[len(chunk):]
		f.polls++
		for _, r := range chunk {
			if f.failAt == 0 {
				return f.readErr
			}
			f.failAt--
			if !fn(r) {
				return nil
			}
		}
	}
	return nil
}

func po(recs []Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = fmt.Sprintf("%d/%d", r.Partition, r.Offset)
	}
	return out
}

func span(p int32, from, to int64) []string { // "p/from" .. "p/to-1"
	var out []string
	for o := from; o < to; o++ {
		out = append(out, fmt.Sprintf("%d/%d", p, o))
	}
	return out
}

func cat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func i32(v int32) *int32 { return &v }

func TestListPagesToHighWatermark(t *testing.T) {
	cases := []struct {
		name      string
		parts     map[int32][2]int64
		late      map[int32]int
		opt       ListOptions
		wantPages [][]string
		wantWarn  []string
	}{
		{name: "1 partition, 25 records, page 10: 3 pages", parts: map[int32][2]int64{0: {0, 25}}, opt: ListOptions{PageSize: 10},
			wantPages: [][]string{span(0, 0, 10), span(0, 10, 20), span(0, 20, 25)}},
		{name: "3 partitions, one empty: union is every record; a full last page is end", parts: map[int32][2]int64{0: {0, 4}, 1: {7, 0}, 2: {2, 5}},
			opt: ListOptions{PageSize: 3}, wantPages: [][]string{span(0, 0, 3), cat(span(0, 3, 4), span(2, 2, 4)), span(2, 4, 7)}},
		{name: "a page that fills at a partition's end before empty partitions is end", parts: map[int32][2]int64{0: {0, 3}, 1: {5, 0}, 2: {0, 0}},
			opt: ListOptions{PageSize: 3}, wantPages: [][]string{span(0, 0, 3)}},
		{name: "a page that fills at a partition's end: Next is the next partition's Low (0)", parts: map[int32][2]int64{0: {0, 3}, 1: {0, 3}},
			opt: ListOptions{PageSize: 3}, wantPages: [][]string{span(0, 0, 3), span(1, 0, 3)}},
		{name: "a page that fills at a partition's end: Next is the next partition's Low (5), no warning", parts: map[int32][2]int64{0: {0, 3}, 1: {5, 2}},
			opt: ListOptions{PageSize: 3}, wantPages: [][]string{span(0, 0, 3), span(1, 5, 7)}},
		{name: "a page that fills at a partition's end: Next skips an empty partition", parts: map[int32][2]int64{0: {0, 2}, 1: {9, 0}, 2: {4, 3}},
			opt: ListOptions{PageSize: 2}, wantPages: [][]string{span(0, 0, 2), span(2, 4, 6), span(2, 6, 7)}},
		{name: "From at Low (Low > 0) is not an expiry", parts: map[int32][2]int64{0: {4, 3}},
			opt: ListOptions{From: Cursor{Partition: 0, Offset: 4, set: true}}, wantPages: [][]string{span(0, 4, 7)}},
		{name: "From at a partition's High goes on to later partitions", parts: map[int32][2]int64{0: {0, 3}, 1: {0, 2}},
			opt: ListOptions{From: Cursor{Partition: 0, Offset: 3, set: true}}, wantPages: [][]string{span(1, 0, 2)}},
		{name: "Partition on an empty partition is one empty page and end", parts: map[int32][2]int64{0: {0, 3}, 1: {6, 0}},
			opt: ListOptions{Partition: i32(1)}, wantPages: [][]string{{}}},
		{name: "records appended after the snapshot are not returned", parts: map[int32][2]int64{0: {0, 5}, 1: {0, 2}}, late: map[int32]int{0: 3, 1: 4},
			opt: ListOptions{PageSize: 4}, wantPages: [][]string{span(0, 0, 4), cat(span(0, 4, 5), span(1, 0, 2))}},
		{name: "Partition restricts the listing", parts: map[int32][2]int64{0: {0, 5}, 1: {0, 5}, 2: {3, 4}}, opt: ListOptions{Partition: i32(2), PageSize: 3},
			wantPages: [][]string{span(2, 3, 6), span(2, 6, 7)}},
		{name: "From below Low warns and starts at Low", parts: map[int32][2]int64{0: {0, 2}, 1: {10, 5}, 2: {0, 2}},
			opt: ListOptions{From: Cursor{Partition: 1, Offset: 3, set: true}}, wantPages: [][]string{cat(span(1, 10, 15), span(2, 0, 2))},
			wantWarn: []string{"WARNING partition 1: offsets 3..9 expired"}},
		{name: "From mid-partition skips earlier partitions and offsets", parts: map[int32][2]int64{0: {0, 9}, 1: {0, 9}},
			opt: ListOptions{From: Cursor{Partition: 0, Offset: 7, set: true}, PageSize: 5}, wantPages: [][]string{cat(span(0, 7, 9), span(1, 0, 3)), span(1, 3, 8), span(1, 8, 9)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeReader(tc.parts)
			f.late = tc.late
			m := NewManager(f, nil, nil)
			opt := tc.opt
			var pages [][]string
			var warns []string
			for i := 0; i < 10; i++ { // a bug that never reaches the end fails the page count instead of hanging
				page, err := m.List(context.Background(), "t.dlq", opt)
				if err != nil {
					t.Fatalf("page %d: %v", i, err)
				}
				pages, warns = append(pages, po(page.Records)), append(warns, page.Warnings...)
				if !page.Next.IsSet() {
					break
				}
				if i < len(tc.wantPages)-1 && len(tc.wantPages[i+1]) > 0 && page.Next.String() != tc.wantPages[i+1][0] {
					t.Errorf("page %d: Next = %s, want %s", i, page.Next, tc.wantPages[i+1][0])
				}
				opt.From, opt.Snapshot = page.Next, page.Watermarks // as dlq list --all: stay on the start-time snapshot
			}
			if !reflect.DeepEqual(pages, tc.wantPages) {
				t.Errorf("pages = %v\nwant    %v", pages, tc.wantPages)
			}
			if !reflect.DeepEqual(warns, tc.wantWarn) {
				t.Errorf("warnings = %q, want %q", warns, tc.wantWarn)
			}
			if tc.wantPages[0] != nil && len(tc.wantPages[0]) > 3 && f.polls <= len(pages) {
				t.Errorf("%d polls for %d pages: the fake should need several reads per page", f.polls, len(pages))
			}
		})
	}
}

func TestListPassesSnapshotHighAsUntil(t *testing.T) {
	f := newFakeReader(map[int32][2]int64{0: {2, 3}, 1: {0, 0}})
	f.late = map[int32]int{0: 5}
	if _, err := NewManager(f, nil, nil).List(context.Background(), "t.dlq", ListOptions{}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"0:2-5", "1:0-0"}; !reflect.DeepEqual(f.reads, want) {
		t.Errorf("Read calls = %v, want %v (until = start-time High)", f.reads, want)
	}
}

func TestListErrorsAreNotEnd(t *testing.T) {
	stall := fmt.Errorf("%w: t.dlq/0 at offset 4", ErrPartitionStalled)
	poll := errors.New("poll: broker gone")
	cases := []struct {
		name     string
		mutate   func(*fakeReader)
		opt      ListOptions
		wantIs   error
		wantText string
		wantRecs int
	}{
		{name: "stall", mutate: func(f *fakeReader) { f.readErr, f.failAt = fmt.Errorf("dlq: read: %w", stall), 4 }, wantIs: ErrPartitionStalled, wantRecs: 4},
		{name: "poll error", mutate: func(f *fakeReader) { f.readErr, f.failAt = fmt.Errorf("dlq: poll t.dlq/1: %w", poll), 0 }, wantIs: poll},
		{name: "missing topic (AC6)", mutate: func(f *fakeReader) {
			f.wmErr = fmt.Errorf("dlq: metadata for no.such.dlq: %w", kerr.UnknownTopicOrPartition)
		}, wantIs: kerr.UnknownTopicOrPartition, wantText: "dlq: topic t.dlq does not exist (check --topic; compose's kafka-init creates the three DLQs)"},
		{name: "other watermark error", mutate: func(f *fakeReader) { f.wmErr = poll }, wantIs: poll},
		{name: "no such --partition", opt: ListOptions{Partition: i32(7)}, wantText: "dlq: partition 7 does not exist (the topic has 2 partitions)"},
		{name: "no such --from partition", opt: ListOptions{From: Cursor{Partition: 9, set: true}}, wantText: "dlq: partition 9 does not exist (the topic has 2 partitions)"},
		{name: "--from outside --partition", opt: ListOptions{Partition: i32(0), From: Cursor{Partition: 1, set: true}}, wantText: "dlq: --from 1/0 is not in --partition 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeReader(map[int32][2]int64{0: {0, 10}, 1: {0, 10}})
			if tc.mutate != nil {
				tc.mutate(f)
			}
			page, err := NewManager(f, nil, nil).List(context.Background(), "t.dlq", tc.opt)
			if err == nil {
				t.Fatalf("err = nil, want an error (records %v, next %v)", po(page.Records), page.Next)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("errors.Is(%v, %v) = false", err, tc.wantIs)
			}
			if tc.wantText != "" && err.Error() != tc.wantText {
				t.Errorf("err = %q, want %q", err, tc.wantText)
			}
			if len(page.Records) != tc.wantRecs || page.Next.IsSet() {
				t.Errorf("page = %v next %v, want %d records read before the error and no next cursor", po(page.Records), page.Next, tc.wantRecs)
			}
		})
	}
}

func TestParseCursor(t *testing.T) {
	for _, s := range []string{"0/0", "3/17", "11/9000000000"} {
		c, err := ParseCursor(s)
		if err != nil || !c.IsSet() || c.String() != s {
			t.Errorf("ParseCursor(%q) = %v (set %v), %v; want it back unchanged", s, c, c.IsSet(), err)
		}
	}
	for _, s := range []string{"", "3", "3/", "/5", "a/b", "-1/0", "0/-1", "3/5/7", "3 /5", "2147483648/0"} {
		if c, err := ParseCursor(s); err == nil || c.IsSet() || !strings.Contains(err.Error(), "want P/O") {
			t.Errorf("ParseCursor(%q) = %v, %v; want a P/O error", s, c, err)
		}
	}
}

// fakePublisher records every publish; failOn (1-based call number, 0 = never) returns an error instead.
type fakePublisher struct {
	calls  int
	failOn int
	sent   []string // "P/O=event_id" of each acked publish, from the envelope's payload marker
	ids    map[string]string
}

func (f *fakePublisher) Publish(_ context.Context, topic string, e events.EventEnvelope) error {
	f.calls++
	if f.calls == f.failOn {
		return errors.New("broker down")
	}
	po, _ := e.Payload["po"].(string)
	f.sent = append(f.sent, po)
	if f.ids == nil {
		f.ids = map[string]string{}
	}
	f.ids[po+"#"+fmt.Sprint(len(f.sent))] = e.EventID
	if topic != "papers.downloaded" || e.EventID == "" || e.Attempt != 0 {
		return fmt.Errorf("bad publish to %s: %+v", topic, e)
	}
	return nil
}

// fakeStore is an in-memory Store: per-partition checkpoints and a log of resolved offsets.
type fakeStore struct {
	cps         map[int32]int64
	log         map[string]Resolution // "P/O"
	marks       int
	failMarkAt  string // "P/O": MarkResolved for that record fails (once)
	cpErr       error
	resolvedErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{cps: map[int32]int64{}, log: map[string]Resolution{}}
}

func (s *fakeStore) Checkpoints(context.Context, string) (map[int32]int64, error) {
	out := map[int32]int64{}
	for p, o := range s.cps {
		out[p] = o
	}
	return out, s.cpErr
}

func (s *fakeStore) Resolved(_ context.Context, _ string, p int32, from int64) (map[int64]bool, error) {
	if s.resolvedErr != nil {
		return nil, s.resolvedErr
	}
	out := map[int64]bool{}
	for _, r := range s.log {
		if r.Partition == p && r.Offset >= from {
			out[r.Offset] = true
		}
	}
	return out, nil
}

func (s *fakeStore) MarkResolved(_ context.Context, r Resolution) error {
	s.marks++
	if k := fmt.Sprintf("%d/%d", r.Partition, r.Offset); r.Outcome != "" && k == s.failMarkAt {
		s.failMarkAt = ""
		return errors.New("connection reset")
	}
	if r.Outcome != "" {
		s.log[fmt.Sprintf("%d/%d", r.Partition, r.Offset)] = r
	}
	if r.Next > s.cps[r.Partition] {
		s.cps[r.Partition] = r.Next
	}
	return nil
}

// dlqTopic builds a fake DLQ: spec per partition, one letter per record. A/B: full envelopes with that error_type;
// U: nil original_value (unreplayable); X: undecodable bytes.
func dlqTopic(spec map[int32]string) *fakeReader {
	f := &fakeReader{low: map[int32]int64{}, log: map[int32][]Record{}, failAt: -1}
	for p, kinds := range spec {
		f.log[p] = []Record{}
		for o, k := range kinds {
			po := fmt.Sprintf("%d/%d", p, o)
			r := Record{Partition: p, Offset: int64(o), Timestamp: time.UnixMilli(1764000000000 + int64(o)), Value: []byte("{}")}
			pl := &events.DLQPayload{OriginalTopic: "papers.downloaded", OriginalKey: "2401.0000" + fmt.Sprint(o), ErrorType: string(k)}
			switch k {
			case 'A', 'B':
				pl.OriginalValue = map[string]interface{}{"event_id": "ev-" + po, "event_type": "papers.downloaded",
					"paper_id": pl.OriginalKey, "attempt": float64(3), "payload": map[string]interface{}{"po": po}}
			case 'X':
				pl = nil
			}
			r.Payload = pl
			f.log[p] = append(f.log[p], r)
		}
	}
	return f
}

func runReplay(t *testing.T, r *fakeReader, pub *fakePublisher, st *fakeStore, opt ReplayOptions) (ReplayResult, error) {
	t.Helper()
	return NewManager(r, pub, st).Replay(context.Background(), "papers.downloaded.dlq", opt)
}

// AC3: two runs of --limit N over 2N records publish 1..N then N+1..2N in (partition, offset) order; a third publishes 0.
func TestReplayResumes(t *testing.T) {
	r, pub, st := dlqTopic(map[int32]string{0: "AAAA", 1: "AA"}), &fakePublisher{}, newFakeStore()
	want := [][]string{{"0/0", "0/1", "0/2"}, {"0/3", "1/0", "1/1"}, nil}
	for run, w := range want {
		before := len(pub.sent)
		res, err := runReplay(t, r, pub, st, ReplayOptions{Limit: 3})
		if got := pub.sent[before:]; err != nil || !reflect.DeepEqual(got, w) && !(len(got) == 0 && len(w) == 0) || res.Published != len(w) {
			t.Fatalf("run %d: err %v, published %v (count %d), want %v", run+1, err, got, res.Published, w)
		}
	}
	if !reflect.DeepEqual(st.cps, map[int32]int64{0: 4, 1: 2}) || len(st.log) != 6 {
		t.Errorf("checkpoints %v, %d log rows; want {0:4 1:2} = High, 6", st.cps, len(st.log))
	}
}

// AC4: MarkResolved fails right after record k was acked. The checkpoint stays at k, and the next run publishes k
// again with the identical event_id, then carries on.
func TestReplayCrashAfterPublish(t *testing.T) {
	for _, kind := range []string{"AAAAA"} { // B-PIPE-2 adds a raw-payload row (deterministic ULID)
		t.Run(kind, func(t *testing.T) {
			r, pub, st := dlqTopic(map[int32]string{0: kind}), &fakePublisher{}, newFakeStore()
			st.failMarkAt = "0/2"
			if _, err := runReplay(t, r, pub, st, ReplayOptions{}); err == nil || !strings.Contains(err.Error(), "mark 0/2 resolved") {
				t.Fatalf("run 1 err = %v, want the MarkResolved error for 0/2", err)
			}
			if st.cps[0] != 2 || st.log["0/2"].Outcome != "" || !reflect.DeepEqual(pub.sent, []string{"0/0", "0/1", "0/2"}) {
				t.Fatalf("after run 1: checkpoint %d (want 2), 0/2 logged %v, sent %v", st.cps[0], st.log["0/2"].Outcome != "", pub.sent)
			}
			first := pub.ids["0/2#3"]
			if _, err := runReplay(t, r, pub, st, ReplayOptions{}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(pub.sent, []string{"0/0", "0/1", "0/2", "0/2", "0/3", "0/4"}) || pub.ids["0/2#4"] != first || first == "" {
				t.Errorf("run 2 sent %v, 0/2 ids %q then %q; want 0/2 again with the same id, then 0/3, 0/4", pub.sent, first, pub.ids["0/2#4"])
			}
			if st.cps[0] != 5 || st.log["0/2"].EventID != first {
				t.Errorf("checkpoint %d, logged id %q; want 5 and %q", st.cps[0], st.log["0/2"].EventID, first)
			}
		})
	}
	// A Publish error stops the run before any row: the checkpoint never passes an un-acked publish.
	r, pub, st := dlqTopic(map[int32]string{0: "AAA"}), &fakePublisher{failOn: 2}, newFakeStore()
	if _, err := runReplay(t, r, pub, st, ReplayOptions{}); err == nil || !strings.Contains(err.Error(), "nothing marked resolved") {
		t.Fatalf("err = %v, want the publish error", err)
	}
	if _, ok := st.log["0/1"]; ok || st.cps[0] != 1 || st.marks != 1 {
		t.Fatalf("after a failed publish of 0/1: log %v, checkpoint %d, %d marks; want no 0/1 row, checkpoint 1, 1 mark", st.log, st.cps[0], st.marks)
	}
}

// AC5: --error-type A publishes only As and pins each checkpoint at its first B; a run without the filter publishes
// only the Bs. Unreplayable records are resolved under any filter. Every record is published exactly once overall.
func TestReplayFilterKeepsSkippedEligible(t *testing.T) {
	r, pub, st := dlqTopic(map[int32]string{0: "ABAUBAX", 1: "BA"}), &fakePublisher{}, newFakeStore()
	if _, err := runReplay(t, r, pub, st, ReplayOptions{ErrorType: "a"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pub.sent, []string{"0/0", "0/2", "0/5", "1/1"}) || !reflect.DeepEqual(st.cps, map[int32]int64{0: 1}) {
		t.Fatalf("filtered run: sent %v, checkpoints %v; want the As, checkpoint 0=1 (first B) and none for 1", pub.sent, st.cps)
	}
	if st.log["0/3"].Outcome != OutcomeUnreplayable || st.log["0/6"].Outcome != OutcomeUnreplayable {
		t.Fatalf("unreplayable 0/3 (nil value) and 0/6 (undecodable) not resolved under the filter: %v", st.log)
	}
	if _, err := runReplay(t, r, pub, st, ReplayOptions{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pub.sent[4:], []string{"0/1", "0/4", "1/0"}) || !reflect.DeepEqual(st.cps, map[int32]int64{0: 7, 1: 2}) {
		t.Errorf("unfiltered run: sent %v, checkpoints %v; want only the Bs, then checkpoints at High {0:7 1:2}", pub.sent[4:], st.cps)
	}
	seen := map[string]int{}
	for _, po := range pub.sent {
		if seen[po]++; seen[po] > 1 {
			t.Errorf("%s published twice", po)
		}
	}
	if len(seen) != 7 {
		t.Errorf("%d records published, want 7 (every A and B)", len(seen))
	}
}

// AC6: a dry run publishes and writes nothing, and lists what a real run then publishes, same IDs.
func TestReplayDryRun(t *testing.T) {
	r, pub, st := dlqTopic(map[int32]string{0: "AUAAX", 1: "BX"}), &fakePublisher{}, newFakeStore()
	st.cps[0] = 1
	dry, err := runReplay(t, r, nil, st, ReplayOptions{DryRun: true})
	if err != nil || pub.calls != 0 || st.marks != 0 || !reflect.DeepEqual(st.cps, map[int32]int64{0: 1}) {
		t.Fatalf("dry run: err %v, %d publishes, %d marks, checkpoints %v; want 0, 0, unchanged", err, pub.calls, st.marks, st.cps)
	}
	real, err := runReplay(t, r, pub, st, ReplayOptions{})
	if err != nil || dry.Published != pub.calls || dry.Published != 3 || !reflect.DeepEqual(dry.Items, real.Items) {
		t.Errorf("dry %d items %+v\nreal %d publishes, items %+v", dry.Published, dry.Items, pub.calls, real.Items)
	}
	if it := dry.Items[0]; it.Partition != 0 || it.Offset != 1 || it.Outcome != OutcomeUnreplayable {
		t.Errorf("first dry-run item %+v, want 0/1 unreplayable (0/0 is below the checkpoint)", it)
	}
	if it := dry.Items[1]; it.Offset != 2 || it.EventID != "ev-0/2" {
		t.Errorf("dry-run item %+v, want 0/2 with event_id ev-0/2", it)
	}
}

// AC7: Postgres down or not migrated stops Replay, real or dry, before any Kafka read or publish.
func TestReplayNeedsMigratedStore(t *testing.T) {
	pub := &fakePublisher{}
	for _, dry := range []bool{false, true} {
		r, st := dlqTopic(map[int32]string{0: "AA"}), newFakeStore()
		st.cpErr = errors.New(`relation "dlq_replay_checkpoints" does not exist`)
		_, err := NewManager(r, pub, st).Replay(context.Background(), "t.dlq", ReplayOptions{DryRun: dry})
		if err == nil || !strings.Contains(err.Error(), "run 'arxivlens migrate'") || r.wmCalls != 0 || len(r.reads) != 0 || pub.calls != 0 {
			t.Errorf("dry=%v not migrated: err %v, %d Watermarks, %d reads, %d publishes; want a migrate hint before any Kafka read", dry, err, r.wmCalls, len(r.reads), pub.calls)
		}
	}
}

// Expired offsets warn and move the checkpoint to Low; a stall keeps every checkpoint already written and is an error.
func TestReplayExpiredAndStall(t *testing.T) {
	r, pub, st := dlqTopic(map[int32]string{0: "AAAAA"}), &fakePublisher{}, newFakeStore()
	r.low[0], r.log[0] = 3, r.log[0][3:]
	st.cps[0] = 1
	res, err := runReplay(t, r, pub, st, ReplayOptions{})
	if err != nil || !reflect.DeepEqual(res.Warnings, []string{"WARNING partition 0: offsets 1..2 expired before replay"}) ||
		!reflect.DeepEqual(pub.sent, []string{"0/3", "0/4"}) || st.cps[0] != 5 || !reflect.DeepEqual(r.reads, []string{"0:3-5"}) {
		t.Fatalf("err %v, warnings %v, sent %v, checkpoint %d, reads %v; want the reader to start at Low (0:3-5)", err, res.Warnings, pub.sent, st.cps[0], r.reads)
	}
	// Every record expired (checkpoint 1 < Low = High = 3): a real run moves the checkpoint to Low, a dry run writes nothing.
	for _, dry := range []bool{false, true} {
		r, st := dlqTopic(map[int32]string{0: ""}), newFakeStore()
		r.low[0], st.cps[0] = 3, 1
		res, err := runReplay(t, r, &fakePublisher{}, st, ReplayOptions{DryRun: dry})
		want, wantMarks := int64(3), 1
		if dry {
			want, wantMarks = 1, 0
		}
		if err != nil || st.cps[0] != want || st.marks != wantMarks || len(res.Warnings) != 1 || len(r.reads) != 0 {
			t.Errorf("all expired, dry=%v: err %v, checkpoint %d, %d marks, warnings %v, reads %v; want checkpoint %d, %d marks, 1 warning, no read",
				dry, err, st.cps[0], st.marks, res.Warnings, r.reads, want, wantMarks)
		}
	}
	r, pub, st = dlqTopic(map[int32]string{0: "AAAA"}), &fakePublisher{}, newFakeStore()
	r.readErr, r.failAt = fmt.Errorf("read: %w", ErrPartitionStalled), 2
	if _, err := runReplay(t, r, pub, st, ReplayOptions{}); !errors.Is(err, ErrPartitionStalled) || st.cps[0] != 2 {
		t.Errorf("stall: err %v, checkpoint %d; want ErrPartitionStalled and checkpoint 2 kept", err, st.cps[0])
	}
}

// A Resolved error stops the run before anything is published (otherwise resolved records would be re-published).
func TestReplayResolvedErrorStops(t *testing.T) {
	r, pub, st := dlqTopic(map[int32]string{0: "AAA"}), &fakePublisher{}, newFakeStore()
	st.log["0/1"] = Resolution{Partition: 0, Offset: 1, Outcome: OutcomeReplayed}
	st.resolvedErr = errors.New("connection reset")
	if _, err := runReplay(t, r, pub, st, ReplayOptions{}); err == nil || !strings.Contains(err.Error(), "connection reset") || pub.calls != 0 || st.marks != 0 {
		t.Errorf("err %v, %d publishes, %d marks; want the Resolved error, 0 publishes, 0 marks", err, pub.calls, st.marks)
	}
}

// AC6: a dry run whose partition ends on already-logged records moves no checkpoint (a real run does).
func TestReplayDryRunTailNoWrite(t *testing.T) {
	for _, dry := range []bool{true, false} {
		r, st := dlqTopic(map[int32]string{0: "AAA"}), newFakeStore()
		st.log["0/1"] = Resolution{Partition: 0, Offset: 1, Outcome: OutcomeReplayed}
		st.log["0/2"] = Resolution{Partition: 0, Offset: 2, Outcome: OutcomeReplayed}
		st.cps[0] = 1
		_, err := runReplay(t, r, &fakePublisher{}, st, ReplayOptions{DryRun: dry})
		if want := map[bool]int64{true: 1, false: 3}[dry]; err != nil || st.cps[0] != want || dry && st.marks != 0 {
			t.Errorf("dry=%v: err %v, checkpoint %d, %d marks; want checkpoint %d (0 marks in a dry run)", dry, err, st.cps[0], st.marks, want)
		}
	}
}

// --limit counts published records only: unreplayable ones before them don't use it up.
func TestReplayLimitCountsPublishedOnly(t *testing.T) {
	r, pub, st := dlqTopic(map[int32]string{0: "UXAA"}), &fakePublisher{}, newFakeStore()
	res, err := runReplay(t, r, pub, st, ReplayOptions{Limit: 1})
	if err != nil || res.Published != 1 || !reflect.DeepEqual(pub.sent, []string{"0/2"}) || len(res.Items) != 3 {
		t.Errorf("err %v, published %d, sent %v, %d items; want 0/2 published after 2 unreplayables", err, res.Published, pub.sent, len(res.Items))
	}
}

// A payload without original_topic is replayed to the DLQ name minus ".dlq", never back into the DLQ.
func TestReplayTargetFallback(t *testing.T) {
	r, pub, st := dlqTopic(map[int32]string{0: "A"}), &fakePublisher{}, newFakeStore()
	r.log[0][0].Payload.OriginalTopic = ""
	res, err := runReplay(t, r, pub, st, ReplayOptions{})
	if err != nil || len(res.Items) != 1 || res.Items[0].Target != "papers.downloaded" || pub.calls != 1 {
		t.Errorf("err %v, items %+v; want one publish to papers.downloaded", err, res.Items)
	}
}
