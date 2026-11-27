package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"arxivlens/internal/config"
	"arxivlens/internal/dlq"
	"arxivlens/shared/events"
	"github.com/twmb/franz-go/pkg/kerr"
)

// fakeDLQ is an in-memory topic behind the CLI's reader factory (no Kafka). late records are appended to partition 0
// right after the first Watermarks call, so a run that re-snapshots would list them.
type fakeDLQ struct {
	low       map[int32]int64
	log       map[int32][]dlq.Record
	late      int
	wmErr     error
	readErr   error // returned by Read once failAfter records were delivered (-1: never)
	failAfter int
	wmCalls   int
	reads     []string // "p:from-until"
	closed    int
}

func newFakeDLQ(counts map[int32]int) *fakeDLQ {
	f := &fakeDLQ{low: map[int32]int64{}, log: map[int32][]dlq.Record{}, failAfter: -1}
	for p, n := range counts {
		f.log[p] = nil
		f.add(p, n)
	}
	return f
}

func (f *fakeDLQ) add(p int32, n int) {
	for i := 0; i < n; i++ {
		o := f.low[p] + int64(len(f.log[p]))
		f.log[p] = append(f.log[p], dlq.Record{Partition: p, Offset: o, Value: []byte("{}"),
			Payload: &events.DLQPayload{OriginalKey: fmt.Sprintf("k%d-%d", p, o), ErrorType: "E"}})
	}
}

func (f *fakeDLQ) Watermarks(context.Context, string) (map[int32]dlq.Watermark, error) {
	f.wmCalls++
	if f.wmErr != nil {
		return nil, f.wmErr
	}
	wm := map[int32]dlq.Watermark{}
	for p, recs := range f.log {
		wm[p] = dlq.Watermark{Low: f.low[p], High: f.low[p] + int64(len(recs))}
	}
	f.add(0, f.late)
	f.late = 0
	return wm, nil
}

func (f *fakeDLQ) Read(_ context.Context, _ string, p int32, from, until int64, fn func(dlq.Record) bool) error {
	f.reads = append(f.reads, fmt.Sprintf("%d:%d-%d", p, from, until))
	for _, r := range f.log[p] {
		if r.Offset < from || r.Offset >= until {
			continue
		}
		if f.failAfter == 0 {
			return f.readErr
		}
		f.failAfter--
		if !fn(r) {
			return nil
		}
	}
	return nil
}

func (f *fakeDLQ) Close() { f.closed++ }

// runDLQ executes `arxivlens dlq <args>` (the production root command) with f behind the reader factory and returns
// stdout, stderr and the error. Replay gets st and a publisher that records into pub (both may be nil for list/inspect).
func runDLQ(f *fakeDLQ, factoryErr error, args ...string) (string, string, error) {
	return runDLQReplay(f, factoryErr, nil, nil, args...)
}

func runDLQReplay(f *fakeDLQ, factoryErr error, st *cliStore, pub *cliPub, args ...string) (string, string, error) {
	cmd := newRootCmd(newDLQCmdWith(func() (dlqReader, error) {
		if factoryErr != nil {
			return nil, factoryErr
		}
		return f, nil
	}, func(context.Context) (dlq.Publisher, dlq.Store, func(), error) { return pub, st, func() {}, nil }))
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"dlq"}, args...))
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestDLQFlags(t *testing.T) {
	const missing = "dlq: topic no.such.dlq does not exist (check --topic; compose's kafka-init creates the three DLQs)"
	stall := fmt.Errorf("dlq: read: %w", dlq.ErrPartitionStalled)
	unknown := fmt.Errorf("dlq: metadata for no.such.dlq: %w", kerr.UnknownTopicOrPartition)
	cases := []struct {
		name       string
		counts     map[int32]int // partition -> records, offsets from 0
		setup      func(*fakeDLQ)
		factoryErr error
		args       []string
		wantErr    string // "" = success; else a substring of err (printed once, by main: stderr has no cobra "Error: " line)
		wantReads  []string
		wantOut    []string // substrings of stdout
		notOut     []string
		wantErrOut []string
		exact      bool // stdout is exactly wantOut[0]
		wmCalls    int
		closed     int
	}{
		{name: "default page size is 50", counts: map[int32]int{0: 60}, args: []string{"list"}, wantReads: []string{"0:0-60"},
			wantOut: []string{"=== DLQ topic: papers.downloaded.dlq ===", "0/49  key=k0-49  error=E",
				"next page: arxivlens dlq list --topic papers.downloaded.dlq --from 0/50\n"}, notOut: []string{"0/50  key"}, wmCalls: 1, closed: 1},
		{name: "--page-size", counts: map[int32]int{0: 60}, args: []string{"list", "--page-size", "10", "--topic", "t.dlq"}, wantReads: []string{"0:0-60"},
			wantOut: []string{"0/9  key", "next page: arxivlens dlq list --topic t.dlq --page-size 10 --from 0/10\n"}, notOut: []string{"0/10  key"}, wmCalls: 1, closed: 1},
		{name: "--limit is an alias of --page-size", counts: map[int32]int{0: 60}, args: []string{"list", "--limit", "10"}, wantReads: []string{"0:0-60"},
			wantOut: []string{"0/9  key", "--from 0/10\n"}, notOut: []string{"0/10  key"}, wmCalls: 1, closed: 1},
		{name: "--partition", counts: map[int32]int{0: 3, 1: 3}, args: []string{"list", "--partition", "1"}, wantReads: []string{"1:0-3"},
			wantOut: []string{"1/2  key", "end of topic (high watermarks at start: 0=3 1=3; 3 records)\n"}, notOut: []string{"0/0  key"}, wmCalls: 1, closed: 1},
		{name: "--from P/O then the later partitions", counts: map[int32]int{0: 3, 1: 3, 2: 2}, args: []string{"list", "--from", "1/1"},
			wantReads: []string{"1:1-3", "2:0-2"}, wantOut: []string{"1/1  key", "2/1  key", "end of topic (high watermarks at start: 0=3 1=3 2=2; 4 records)\n"},
			notOut: []string{"1/0  key", "0/2  key"}, wmCalls: 1, closed: 1},
		// AC1 footer: --partition and --page-size are repeated only when given; --limit prints as --page-size.
		{name: "(a) footer keeps --partition and --page-size", counts: map[int32]int{0: 5, 1: 5}, args: []string{"list", "--partition", "0", "--page-size", "2"},
			wantReads: []string{"0:0-5"}, wantOut: []string{"next page: arxivlens dlq list --topic papers.downloaded.dlq --partition 0 --page-size 2 --from 0/2\n"}, wmCalls: 1, closed: 1},
		{name: "(b) footer prints --limit as --page-size", counts: map[int32]int{0: 5}, args: []string{"list", "--limit", "3"},
			wantReads: []string{"0:0-5"}, wantOut: []string{"next page: arxivlens dlq list --topic papers.downloaded.dlq --page-size 3 --from 0/3\n"}, wmCalls: 1, closed: 1},
		{name: "(c) footer without either flag is unchanged", counts: map[int32]int{0: 60}, args: []string{"list", "--from", "0/0"},
			wantReads: []string{"0:0-60"}, wantOut: []string{"next page: arxivlens dlq list --topic papers.downloaded.dlq --from 0/50\n"}, wmCalls: 1, closed: 1},
		{name: "bad --from", counts: map[int32]int{0: 1}, args: []string{"list", "--from", "3-17"}, wantErr: "want P/O"},
		{name: "--all pages on one start-time snapshot", counts: map[int32]int{0: 120}, setup: func(f *fakeDLQ) { f.late = 5 },
			args: []string{"list", "--all"}, wantReads: []string{"0:0-120", "0:50-120", "0:100-120"},
			wantOut: []string{"0/119  key", "end of topic (high watermarks at start: 0=120; 120 records)\n"},
			notOut:  []string{"0/120  key", "next page"}, wmCalls: 1, closed: 1},
		{name: "expired offsets warn on stderr only", counts: map[int32]int{0: 0}, setup: func(f *fakeDLQ) { f.low[0] = 5; f.add(0, 3) },
			args: []string{"list", "--from", "0/2"}, wantReads: []string{"0:5-8"}, wantOut: []string{"0/5  key", "end of topic"},
			notOut: []string{"WARNING"}, wantErrOut: []string{"WARNING partition 0: offsets 2..4 expired\n"}, wmCalls: 1, closed: 1},
		{name: "undecodable record in list", counts: map[int32]int{0: 2}, setup: func(f *fakeDLQ) {
			f.log[0][0] = dlq.Record{Partition: 0, Offset: 0, Value: []byte("garbage")}
		}, args: []string{"list"}, wantReads: []string{"0:0-2"}, wantOut: []string{"0/0  UNDECODABLE (7 bytes)\n", "0/1  key"}, wmCalls: 1, closed: 1},
		{name: "stall is an error, not end", counts: map[int32]int{0: 5}, setup: func(f *fakeDLQ) { f.readErr, f.failAfter = stall, 2 },
			args: []string{"list"}, wantErr: "partition stalled", wantReads: []string{"0:0-5"}, wantOut: []string{"0/1  key"},
			notOut: []string{"end of topic", "next page"}, wmCalls: 1, closed: 1},
		{name: "poll error is an error, not end", counts: map[int32]int{0: 5}, setup: func(f *fakeDLQ) { f.readErr, f.failAfter = errors.New("poll boom"), 0 },
			args: []string{"list", "--all"}, wantErr: "poll boom", wantReads: []string{"0:0-5"}, notOut: []string{"end of topic", "next page"}, wmCalls: 1, closed: 1},
		{name: "missing topic (list)", setup: func(f *fakeDLQ) { f.wmErr = unknown },
			args: []string{"list", "--topic", "no.such.dlq"}, wantErr: missing, notOut: []string{"end of topic"}, wmCalls: 1, closed: 1},
		{name: "missing topic (inspect)", setup: func(f *fakeDLQ) { f.wmErr = unknown },
			args: []string{"inspect", "--topic", "no.such.dlq", "--key", "x"}, wantErr: missing, notOut: []string{"No DLQ events"}, wmCalls: 1, closed: 1},
		{name: "reader factory error", factoryErr: errors.New("no brokers"), args: []string{"list"}, wantErr: "no brokers"},
		{name: "inspect --key scans past 500, first match only", counts: map[int32]int{0: 600, 1: 2},
			setup: func(f *fakeDLQ) { f.log[0][551].Payload = f.log[0][550].Payload },
			args:  []string{"inspect", "--key", "k0-550"}, wantReads: []string{"0:0-600", "0:500-600", "1:0-2"},
			wantOut: []string{strings.Repeat("=", 50) + "\nDLQ Record:     0/550\nOriginal Topic:  (Partition 0, Offset 0)\nPaper Key:      k0-550\n" +
				"Error Type:     E\nError Message:  \nAttempts:       0\nConsumer Group: \nTraceparent:    \nFailed At:      \nStack Trace:\n\n" + strings.Repeat("=", 50) + "\n"}, notOut: []string{"0/551", "No DLQ events"}, wmCalls: 1, closed: 1},
		{name: "inspect pages on one start-time snapshot", counts: map[int32]int{0: 600}, setup: func(f *fakeDLQ) { f.late = 1 },
			args: []string{"inspect", "--key", "k0-600"}, wantReads: []string{"0:0-600", "0:500-600"},
			wantOut: []string{"No DLQ events found for key: k0-600\n"}, wmCalls: 1, closed: 1},
		{name: "inspect without --key prints every record", counts: map[int32]int{0: 2}, setup: func(f *fakeDLQ) {
			f.log[0][1] = dlq.Record{Partition: 0, Offset: 1, Value: []byte("null")}
		}, args: []string{"inspect"}, wantReads: []string{"0:0-2"}, wantOut: []string{"DLQ Record:     0/0\n", "0/1  UNDECODABLE (4 bytes)\n"}, wmCalls: 1, closed: 1},
		{name: "inspect --key skips an undecodable first record", counts: map[int32]int{0: 3}, setup: func(f *fakeDLQ) {
			f.log[0][0] = dlq.Record{Partition: 0, Offset: 0, Value: []byte("garbage")}
		}, args: []string{"inspect", "--key", "k0-2"}, wantReads: []string{"0:0-3"}, wantOut: []string{"DLQ Record:     0/2\n"},
			notOut: []string{"UNDECODABLE", "0/0", "0/1", "No DLQ events"}, wmCalls: 1, closed: 1},
		{name: "inspect on an empty topic", counts: map[int32]int{0: 0, 1: 0}, args: []string{"inspect", "--topic", "t.dlq"},
			wantReads: []string{"0:0-0", "1:0-0"}, wantOut: []string{"No DLQ events in topic: t.dlq\n"}, exact: true, wmCalls: 1, closed: 1},
		{name: "inspect stall is an error", counts: map[int32]int{0: 5}, setup: func(f *fakeDLQ) { f.readErr, f.failAfter = stall, 1 },
			args: []string{"inspect", "--key", "k0-4"}, wantErr: "partition stalled", wantReads: []string{"0:0-5"}, notOut: []string{"No DLQ events"}, wmCalls: 1, closed: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDLQ(tc.counts)
			if tc.setup != nil {
				tc.setup(f)
			}
			out, errOut, err := runDLQ(f, tc.factoryErr, tc.args...)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("err = %v\nstderr: %s", err, errOut)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr) || strings.Contains(errOut, "Error: ")) {
				t.Fatalf("err = %v, stderr %q: want err to contain %q and no \"Error: \" line on stderr", err, errOut, tc.wantErr)
			}
			if !reflect.DeepEqual(f.reads, tc.wantReads) {
				t.Errorf("reads = %v, want %v", f.reads, tc.wantReads)
			}
			for _, s := range tc.wantOut {
				if !strings.Contains(out, s) {
					t.Errorf("stdout lacks %q:\n%s", s, out)
				}
			}
			if tc.exact && out != tc.wantOut[0] {
				t.Errorf("stdout = %q, want exactly %q", out, tc.wantOut[0])
			}
			for _, s := range tc.notOut {
				if strings.Contains(out, s) {
					t.Errorf("stdout has %q:\n%s", s, out)
				}
			}
			for _, s := range tc.wantErrOut {
				if !strings.Contains(errOut, s) {
					t.Errorf("stderr lacks %q: %q", s, errOut)
				}
			}
			if f.wmCalls != tc.wmCalls || f.closed != tc.closed {
				t.Errorf("Watermarks calls = %d, Close calls = %d; want %d, %d", f.wmCalls, f.closed, tc.wmCalls, tc.closed)
			}
		})
	}
}

// AC1 (d): the footer of `--partition 0 --page-size 2`, pasted as-is, reads only partition 0 at page size 2 to the end.
func TestDLQFooterRoundTrip(t *testing.T) {
	f := newFakeDLQ(map[int32]int{0: 4, 1: 3})
	out, _, err := runDLQ(f, nil, "list", "--partition", "0", "--page-size", "2")
	_, footer, ok := strings.Cut(out, "next page: arxivlens dlq ")
	if err != nil || !ok {
		t.Fatalf("first page: err %v, no footer in %q", err, out)
	}
	f.reads = nil
	out, _, err = runDLQ(f, nil, strings.Fields(footer)...)
	if err != nil || !reflect.DeepEqual(f.reads, []string{"0:2-4"}) {
		t.Fatalf("pasted footer %q: err %v, reads %v, want only 0:2-4", footer, err, f.reads)
	}
	if strings.Count(out, "  key=") != 2 || !strings.Contains(out, "0/3  key") || strings.Contains(out, "1/0") ||
		!strings.HasSuffix(out, "end of topic (high watermarks at start: 0=4 1=3; 2 records)\n") {
		t.Errorf("pasted footer output:\n%s\nwant 0/2 and 0/3 only, then end of topic", out)
	}
}

// cliStore / cliPub: minimal replay dependencies for the CLI wiring (the replay logic is tested in internal/dlq).
type cliStore struct{ marks []dlq.Resolution }

func (s *cliStore) Checkpoints(context.Context, string) (map[int32]int64, error) {
	return map[int32]int64{}, nil
}
func (s *cliStore) Resolved(context.Context, string, int32, int64) (map[int64]bool, error) {
	return nil, nil
}
func (s *cliStore) MarkResolved(_ context.Context, r dlq.Resolution) error {
	s.marks = append(s.marks, r)
	return nil
}

type cliPub struct{ envs []events.EventEnvelope }

func (p *cliPub) Publish(_ context.Context, _ string, e events.EventEnvelope) error {
	p.envs = append(p.envs, e)
	return nil
}

func TestDLQReplayCLI(t *testing.T) {
	replayable := func(f *fakeDLQ) { // 0/0 and 0/1 are full envelopes, 0/2 keeps a nil original_value (unreplayable)
		for i, id := range []string{"E1", "E2"} {
			f.log[0][i].Payload = &events.DLQPayload{OriginalTopic: "papers.downloaded", OriginalKey: "2401.0000" + id[1:], ErrorType: "PdfParseError",
				OriginalValue: map[string]interface{}{"event_id": id, "event_type": "papers.downloaded", "paper_id": "2401.0000" + id[1:]}}
		}
	}
	cases := []struct {
		name            string
		args            []string
		wantOut, notOut []string
		marks, publish  int
		reads           []string
	}{
		{name: "dry run lists P/O and event_id, takes no lock", args: []string{"replay", "--dry-run", "--error-type", "pdfparseerror"},
			wantOut: []string{"DRY RUN (no lock taken; may race a concurrent replay)\n", "0/0  would replay -> papers.downloaded  event_id=E1\n",
				"0/1  would replay -> papers.downloaded  event_id=E2\n", "0/2  unreplayable (would be resolved without publishing)",
				"replay summary: published=2 unreplayable=1 dry_run=true\n"}, reads: []string{"0:0-3"}},
		{name: "--limit 1 publishes one", args: []string{"replay", "--limit", "1"}, marks: 1, publish: 1, reads: []string{"0:0-3"},
			wantOut: []string{"=== DLQ replay: papers.downloaded.dlq (checkpoints at start: 0=0; high watermarks at start: 0=3) ===\n",
				"0/0  replayed -> papers.downloaded  event_id=E1\n", "replay summary: published=1 unreplayable=0 dry_run=false\n"}, notOut: []string{"0/1", "DRY RUN"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, st, pub := newFakeDLQ(map[int32]int{0: 3}), &cliStore{}, &cliPub{}
			replayable(f)
			out, errOut, err := runDLQReplay(f, nil, st, pub, tc.args...)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(errOut, "Error: ") {
				t.Errorf("stderr has a cobra Error: line: %q", errOut)
			}
			for _, s := range tc.wantOut {
				if !strings.Contains(out, s) {
					t.Errorf("stdout lacks %q:\n%s", s, out)
				}
			}
			for _, s := range tc.notOut {
				if strings.Contains(out, s) {
					t.Errorf("stdout has %q:\n%s", s, out)
				}
			}
			if len(st.marks) != tc.marks || len(pub.envs) != tc.publish || !reflect.DeepEqual(f.reads, tc.reads) || f.closed != 1 {
				t.Errorf("MarkResolved %d, Publish %d, reads %v, Close %d; want %d, %d, %v, 1", len(st.marks), len(pub.envs), f.reads, f.closed, tc.marks, tc.publish, tc.reads)
			}
		})
	}
}

// AC7: with Postgres unreachable, the real replay wiring fails before reading Kafka and names `arxivlens migrate`.
func TestDLQReplayNeedsPostgres(t *testing.T) {
	saved := cfg
	t.Cleanup(func() { cfg = saved })
	cfg = &config.Config{PostgresURL: "postgres://u:p@127.0.0.1:1/x?sslmode=disable&connect_timeout=2", KafkaBrokers: []string{"127.0.0.1:1"}}
	cmd := newRootCmd(newDLQCmd())
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"dlq", "replay", "--topic", "t.dlq"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "run 'arxivlens migrate'") || out.Len() != 0 {
		t.Fatalf("err = %v, stdout %q: want a Postgres error naming 'arxivlens migrate' and no output", err, out.String())
	}
}
