package dlq

import (
	"encoding/json"
	"fmt"

	"arxivlens/shared/events"
)

// Skip reasons for DLQ records that replay must not publish (design OPS-H7 Q5, PM condition 1).
const (
	skipNilValue   = "original_value is missing (the failed record was not a JSON object)"
	skipEmptyValue = "original_value is an empty object"
	skipRawValue   = "original_value holds only raw_value (undecodable bytes; republishing would fail again)"
	skipNoKey      = "original_value is not a full envelope and original_key is empty (no paper_id to re-envelope with)"
)

// replayEnvelope applies the replay envelope rule to one DLQ record. It returns the envelope to publish to
// targetTopic, or ok=false and a skip reason. A returned envelope always has non-empty EventID, EventType and PaperID.
//   - original_value is a full envelope (event_id, event_type, paper_id set): that envelope, same event_id.
//   - any other non-empty object, with a non-empty original_key: a fresh envelope wrapping the value.
//   - nil, {}, {"raw_value": ...} only, or no original_key: unreplayable.
func replayEnvelope(p events.DLQPayload, targetTopic string) (events.EventEnvelope, string, bool) {
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
	env.Traceparent = p.Traceparent
	return env, "", true
}

// skipLine is the log line for a DLQ record that replay skips (dry-run and real runs alike).
func skipLine(p events.DLQPayload, reason string) string {
	return fmt.Sprintf("[DLQ Replay] Skipped unreplayable event key=%q error_type=%q: %s", p.OriginalKey, p.ErrorType, reason)
}
