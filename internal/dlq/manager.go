package dlq

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"arxivlens/shared/events"
	"github.com/twmb/franz-go/pkg/kerr"
)

// Manager lists and replays one DLQ topic. List needs only the Reader; Replay also needs the Publisher and the Store.
type Manager struct {
	reader Reader
	pub    Publisher
	store  Store
}

// NewManager: pub and store may be nil when only List is called (dlq list / inspect).
func NewManager(reader Reader, pub Publisher, store Store) *Manager {
	return &Manager{reader: reader, pub: pub, store: store}
}

// DefaultPageSize is the page size when ListOptions.PageSize <= 0 (dlq list --page-size default).
const DefaultPageSize = 50

// Cursor is a P/O position (partition P, offset O), the form dlq list prints and --from takes. The zero value is unset.
type Cursor struct {
	Partition int32
	Offset    int64
	set       bool
}

// IsSet is false for the zero Cursor and for Page.Next at end of topic.
func (c Cursor) IsSet() bool { return c.set }

func (c Cursor) String() string { return fmt.Sprintf("%d/%d", c.Partition, c.Offset) }

// ParseCursor parses "P/O": two non-negative integers.
func ParseCursor(s string) (Cursor, error) {
	ps, offs, ok := strings.Cut(s, "/")
	p, errP := strconv.ParseInt(ps, 10, 32)
	o, errO := strconv.ParseInt(offs, 10, 64)
	if !ok || errP != nil || errO != nil || p < 0 || o < 0 {
		return Cursor{}, fmt.Errorf("dlq: cursor %q: want P/O, two non-negative integers such as 3/17", s)
	}
	return Cursor{Partition: int32(p), Offset: o, set: true}, nil
}

// ListOptions: Partition restricts the listing to one partition. From starts at partition P offset O, then the later
// partitions from their Low. Snapshot (nil: List takes one) lets dlq list --all and inspect stop at the run's start-time watermarks.
type ListOptions struct {
	Partition *int32
	From      Cursor
	PageSize  int
	Snapshot  map[int32]Watermark
}

// Page is one page of a listing. Next is unset at end of topic. Warnings (expired offsets) go to stderr.
type Page struct {
	Records    []Record
	Next       Cursor
	Watermarks map[int32]Watermark
	Warnings   []string
}

// List reads up to PageSize records, in partition then offset order, below the start-time high watermarks (design OPS-H7 Q2, Q3).
// A stall or poll error is returned, never treated as end of topic; Page then holds the records read before it.
func (m *Manager) List(ctx context.Context, topic string, opt ListOptions) (Page, error) {
	size, wm := opt.PageSize, opt.Snapshot
	if size <= 0 {
		size = DefaultPageSize
	}
	if wm == nil {
		var err error
		if wm, err = m.reader.Watermarks(ctx, topic); errors.Is(err, kerr.UnknownTopicOrPartition) {
			return Page{}, &missingTopicError{topic, err}
		} else if err != nil {
			return Page{}, err
		}
	}
	page := Page{Watermarks: wm}
	parts, err := listPartitions(wm, opt)
	if err != nil {
		return page, err
	}
	for i, p := range parts {
		from := wm[p].Low
		if opt.From.set && p == opt.From.Partition {
			if from > opt.From.Offset {
				page.Warnings = append(page.Warnings, fmt.Sprintf("WARNING partition %d: offsets %d..%d expired", p, opt.From.Offset, from-1))
			} else {
				from = opt.From.Offset
			}
		}
		err := m.reader.Read(ctx, topic, p, from, wm[p].High, func(r Record) bool {
			page.Records = append(page.Records, r)
			return len(page.Records) < size
		})
		if err != nil {
			return page, err
		}
		if len(page.Records) >= size {
			page.Next = nextCursor(wm, parts[i+1:], page.Records[len(page.Records)-1])
			return page, nil
		}
	}
	return page, nil
}

// listPartitions: the partitions List reads, in order, after the Partition and From filters.
func listPartitions(wm map[int32]Watermark, opt ListOptions) ([]int32, error) {
	var parts []int32
	for p := range wm {
		if (opt.Partition == nil || p == *opt.Partition) && (!opt.From.set || p >= opt.From.Partition) {
			parts = append(parts, p)
		}
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i] < parts[j] })
	if opt.Partition != nil && opt.From.set && *opt.Partition != opt.From.Partition {
		return nil, fmt.Errorf("dlq: --from %s is not in --partition %d", opt.From, *opt.Partition)
	}
	if want := opt.Partition; want != nil || opt.From.set {
		if want == nil {
			want = &opt.From.Partition
		}
		if _, ok := wm[*want]; !ok {
			return nil, fmt.Errorf("dlq: partition %d does not exist (the topic has %d partitions)", *want, len(wm))
		}
	}
	return parts, nil
}

// nextCursor: the position after last, or the Low of the first non-empty partition in rest; unset at end of topic.
func nextCursor(wm map[int32]Watermark, rest []int32, last Record) Cursor {
	if last.Offset+1 < wm[last.Partition].High {
		return Cursor{Partition: last.Partition, Offset: last.Offset + 1, set: true}
	}
	for _, p := range rest {
		if wm[p].Low < wm[p].High {
			return Cursor{Partition: p, Offset: wm[p].Low, set: true}
		}
	}
	return Cursor{}
}

// missingTopicError: AC6 message; it wraps kerr.UnknownTopicOrPartition from Watermarks.
type missingTopicError struct {
	topic string
	err   error
}

func (e *missingTopicError) Error() string {
	return fmt.Sprintf("dlq: topic %s does not exist (check --topic; compose's kafka-init creates the three DLQs)", e.topic)
}

func (e *missingTopicError) Unwrap() error { return e.err }

// Publisher publishes an envelope and returns only after the broker acked it (*kafka.Producer: ProduceSync).
type Publisher interface {
	Publish(ctx context.Context, topic string, e events.EventEnvelope) error
}

// Store keeps replay state in Postgres (design OPS-H7 Q1): a per-partition checkpoint (every offset below it is
// resolved) and one log row per resolved DLQ record. *PGStore in production, a fake in tests.
type Store interface {
	Checkpoints(ctx context.Context, dlqTopic string) (map[int32]int64, error)
	// Resolved: the offsets >= from of partition p that have a log row.
	Resolved(ctx context.Context, dlqTopic string, p int32, from int64) (map[int64]bool, error)
	// MarkResolved writes r's log row (none when r.Outcome is "") and, when r.Next > 0, moves the partition's
	// checkpoint up to r.Next (never down), in one transaction.
	MarkResolved(ctx context.Context, r Resolution) error
}

// Resolution outcomes (dlq_replay_log.outcome).
const (
	OutcomeReplayed     = "replayed"
	OutcomeUnreplayable = "unreplayable"
)

// Resolution is one MarkResolved call. Outcome "" moves only the checkpoint (records that were already resolved).
type Resolution struct {
	DLQTopic    string
	Partition   int32
	Offset      int64
	Outcome     string
	TargetTopic string
	EventID     string
	Next        int64 // new checkpoint; 0 = unchanged (a skipped record earlier in the run pins it)
}

// ReplayOptions: ErrorType filters (case-insensitive); Limit caps published records across partitions (<= 0: no cap).
type ReplayOptions struct {
	ErrorType string
	Limit     int
	DryRun    bool
}

// ReplayItem is one record the run published (or would publish, in a dry run) or resolved as unreplayable.
type ReplayItem struct {
	Partition int32
	Offset    int64
	Outcome   string
	Target    string
	EventID   string
	Reason    string // unreplayable only
}

// ReplayResult: Published counts replayed records (would-be, in a dry run). Checkpoints are the ones read at the start.
type ReplayResult struct {
	Published   int
	Items       []ReplayItem
	Checkpoints map[int32]int64
	Watermarks  map[int32]Watermark
	Warnings    []string
}

// Replay republishes the unresolved records of a DLQ topic, in (partition, offset) order, up to the high watermarks
// taken at the start (design OPS-H7 Q1-Q7). Per record: Publish, and only after the ack MarkResolved, so the checkpoint
// never passes an un-acked publish; a crash between the two re-publishes the record (a full envelope keeps its event_id).
// A record the ErrorType filter skips gets no row and pins its partition's checkpoint, so it stays eligible. A dry run
// writes nothing. Two concurrent runs of the same DLQ are not yet prevented (lock: B-PIPE-2).
func (m *Manager) Replay(ctx context.Context, topic string, opt ReplayOptions) (res ReplayResult, err error) {
	if m.reader == nil || m.store == nil || (m.pub == nil && !opt.DryRun) {
		return res, errors.New("dlq: Replay needs a Reader, a Store and (unless --dry-run) a Publisher")
	}
	if res.Checkpoints, err = m.store.Checkpoints(ctx, topic); err != nil {
		return res, storeError(err) // before any Kafka read
	}
	if res.Watermarks, err = m.reader.Watermarks(ctx, topic); errors.Is(err, kerr.UnknownTopicOrPartition) {
		return res, &missingTopicError{topic, err}
	} else if err != nil {
		return res, err
	}
	parts := make([]int32, 0, len(res.Watermarks))
	for p := range res.Watermarks {
		parts = append(parts, p)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i] < parts[j] })
	for _, p := range parts {
		if done, err := m.replayPartition(ctx, topic, p, opt, &res); err != nil || done {
			return res, err
		}
	}
	return res, nil
}

// replayPartition replays one partition from its checkpoint; done is true once opt.Limit records were published.
func (m *Manager) replayPartition(ctx context.Context, topic string, p int32, opt ReplayOptions, res *ReplayResult) (done bool, err error) {
	wm, cp := res.Watermarks[p], res.Checkpoints[p]
	from := cp
	if from < wm.Low { // retention deleted records that were never resolved (design Q2)
		res.Warnings = append(res.Warnings, fmt.Sprintf("WARNING partition %d: offsets %d..%d expired before replay", p, from, wm.Low-1))
		from = wm.Low
	}
	if from >= wm.High {
		if from > cp && !opt.DryRun { // every record left has expired: start the next run at Low
			err = m.store.MarkResolved(context.WithoutCancel(ctx), Resolution{DLQTopic: topic, Partition: p, Next: from})
		}
		return false, err
	}
	resolved, err := m.store.Resolved(ctx, topic, p, from)
	if err != nil {
		return false, storeError(err)
	}
	contiguous, next, written := true, from, cp // next: checkpoint the resolved prefix allows; written: stored value
	mark := func(r Resolution) error {
		if contiguous {
			r.Next, next, written = r.Offset+1, r.Offset+1, r.Offset+1
		}
		if opt.DryRun {
			return nil
		}
		// WithoutCancel: a Ctrl-C right after a publish ack still records it, so the next run doesn't re-publish it.
		if err := m.store.MarkResolved(context.WithoutCancel(ctx), r); err != nil {
			return fmt.Errorf("dlq: mark %d/%d resolved (a published record is re-published by the next run): %w",
				r.Partition, r.Offset, storeError(err))
		}
		return nil
	}
	var stepErr error
	err = m.reader.Read(ctx, topic, p, from, wm.High, func(r Record) bool {
		if resolved[r.Offset] {
			if contiguous {
				next = r.Offset + 1
			}
			return true
		}
		item := ReplayItem{Partition: p, Offset: r.Offset, Outcome: OutcomeUnreplayable, Reason: "the DLQ record is not a DLQ payload (undecodable)"}
		var env events.EventEnvelope
		if r.Payload != nil {
			item.Target = r.Payload.OriginalTopic
			if item.Target == "" {
				item.Target = strings.TrimSuffix(topic, ".dlq")
			}
			var ok bool
			if env, item.Reason, ok = replayEnvelope(*r.Payload, item.Target); ok {
				if opt.ErrorType != "" && !strings.EqualFold(r.Payload.ErrorType, opt.ErrorType) {
					contiguous = false // filtered out: no row, stays eligible, pins the checkpoint (design Q6)
					return true
				}
				item.Outcome, item.EventID = OutcomeReplayed, env.EventID
			}
		}
		if item.Outcome == OutcomeReplayed && !opt.DryRun {
			if stepErr = m.pub.Publish(ctx, item.Target, env); stepErr != nil {
				stepErr = fmt.Errorf("dlq: publish %d/%d to %s (nothing marked resolved): %w", p, r.Offset, item.Target, stepErr)
				return false
			}
		}
		if stepErr = mark(Resolution{DLQTopic: topic, Partition: p, Offset: r.Offset, Outcome: item.Outcome,
			TargetTopic: item.Target, EventID: item.EventID}); stepErr != nil {
			return false
		}
		res.Items = append(res.Items, item)
		if item.Outcome == OutcomeReplayed {
			res.Published++
		}
		done = opt.Limit > 0 && res.Published >= opt.Limit
		return !done
	})
	if err == nil {
		err = stepErr
	}
	if err == nil && !done && !opt.DryRun && contiguous && next > written {
		// The partition ended on already-resolved records: move the checkpoint over them.
		err = m.store.MarkResolved(context.WithoutCancel(ctx), Resolution{DLQTopic: topic, Partition: p, Next: next})
	}
	return done, err
}

// storeError points at `arxivlens migrate` when the replay tables or Postgres itself are not there.
func storeError(err error) error {
	return fmt.Errorf("dlq replay needs a migrated PostgreSQL (DATABASE_URL; run 'arxivlens migrate'): %w", err)
}
