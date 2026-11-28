package dlq

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"arxivlens/shared/events"
	"github.com/oklog/ulid/v2"
)

// Skip reasons for DLQ records that replay must not publish (design OPS-H7 Q5, PM condition 1).
const (
	skipNilValue   = "original_value is missing (the failed record was not a JSON object)"
	skipEmptyValue = "original_value is an empty object"
	skipRawValue   = "original_value holds only raw_value (undecodable bytes; republishing would fail again)"
	skipNoKey      = "original_value is not a full envelope and original_key is empty (no paper_id to re-envelope with)"
)

// source names the DLQ record a replayed envelope comes from: its topic, partition, offset and Kafka timestamp.
type source struct {
	Topic     string
	Partition int32
	Offset    int64
	Timestamp time.Time
}

// replayID is the deterministic ULID of a re-enveloped DLQ record (design OPS-H7 Q5): the time part is the DLQ record's
// Kafka timestamp in ms, the entropy is the first 10 bytes of SHA-256 over the record's (topic, partition, offset). The same
// record always gets the same ID, so a re-publish after a crash dedupes in the processor's ledger.
func replayID(s source) string {
	h := sha256.Sum256([]byte("arxivlens/dlq-replay\x00" + s.Topic + "\x00" + strconv.Itoa(int(s.Partition)) + "\x00" +
		strconv.FormatInt(s.Offset, 10)))
	var ms uint64 // a record without a timestamp (before 1970) gets time part 0, never a panic
	if s.Timestamp.UnixMilli() > 0 {
		ms = ulid.Timestamp(s.Timestamp)
	}
	return ulid.MustNew(ms, bytes.NewReader(h[:10])).String()
}

// replayEnvelope applies the replay envelope rule to one DLQ record. It returns the envelope to publish to
// targetTopic, or ok=false and a skip reason. A returned envelope always has non-empty EventID, EventType and PaperID.
//   - original_value is a full envelope (event_id, event_type, paper_id set): that envelope, same event_id.
//   - any other non-empty object, with a non-empty original_key: a new envelope wrapping the value, with the
//     deterministic replayID(src) and occurred_at = the DLQ record's timestamp, so every replay of it is identical.
//   - nil, {}, {"raw_value": ...} only, or no original_key: unreplayable.
func replayEnvelope(p events.DLQPayload, targetTopic string, src source) (events.EventEnvelope, string, bool) {
	v := p.OriginalValue
	if v == nil {
		return events.EventEnvelope{}, skipNilValue, false
	}
	if len(v) == 0 {
		return events.EventEnvelope{}, skipEmptyValue, false
	}
	if _, raw := v["raw_value"]; raw && len(v) == 1 {
		return events.EventEnvelope{}, skipRawValue, false
	}

	var env events.EventEnvelope
	if b, err := json.Marshal(v); err == nil && json.Unmarshal(b, &env) == nil &&
		env.EventID != "" && env.EventType != "" && env.PaperID != "" {
		env.Attempt = 0 // Reset attempt counter (§6)
		env.Traceparent = p.Traceparent
		return env, "", true
	}

	if p.OriginalKey == "" {
		return events.EventEnvelope{}, skipNoKey, false
	}
	env = events.NewEventEnvelope(targetTopic, p.OriginalKey, 1, v)
	env.EventID, env.OccurredAt = replayID(src), src.Timestamp.UTC().Format(time.RFC3339Nano)
	env.Traceparent = p.Traceparent
	return env, "", true
}

// skipLine is the log line for a DLQ record that replay skips (dry-run and real runs alike).
func skipLine(p events.DLQPayload, reason string) string {
	return fmt.Sprintf("[DLQ Replay] Skipped unreplayable event key=%q error_type=%q: %s", p.OriginalKey, p.ErrorType, reason)
}
