package dlq

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

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
			m := NewManager(nil, f)
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
	if _, err := NewManager(nil, f).List(context.Background(), "t.dlq", ListOptions{}); err != nil {
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
			page, err := NewManager(nil, f).List(context.Background(), "t.dlq", tc.opt)
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
