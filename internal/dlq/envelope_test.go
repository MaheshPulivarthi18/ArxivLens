package dlq

import (
	"reflect"
	"strings"
	"testing"

	"arxivlens/shared/events"
	"github.com/oklog/ulid/v2"
)

const (
	testTarget = events.TopicPapersDiscovered
	testTrace  = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	testID     = "01JC0000000000000000000000"
)

type envelopeCase struct {
	name   string
	in     events.DLQPayload
	want   string // "keep" (full envelope), "wrap" (re-envelope) or "skip"
	reason string // skip reason when want == "skip"
}

func dlqRec(key string, value map[string]interface{}) events.DLQPayload {
	return events.DLQPayload{OriginalTopic: testTarget, OriginalKey: key, OriginalValue: value,
		ErrorType: "ValueError", Attempts: 3, Traceparent: testTrace}
}

func envelopeCases() []envelopeCase {
	full := map[string]interface{}{"event_id": testID, "event_type": testTarget, "occurred_at": "2026-11-07T00:00:00Z",
		"paper_id": "2401.00001", "version": float64(1), "attempt": float64(3), "traceparent": "00-old-old-01",
		"payload": map[string]interface{}{"arxiv_id": "2401.00001"}}
	raw := map[string]interface{}{"arxiv_id": "2401.00001", "version": float64(1)}
	return []envelopeCase{
		{name: "a full envelope", in: dlqRec("2401.00001", full), want: "keep"},
		{name: "a full envelope, empty key", in: dlqRec("", full), want: "keep"},
		{name: "b raw object", in: dlqRec("2401.00001", raw), want: "wrap"},
		{name: "b partial envelope", in: dlqRec("2401.00001", map[string]interface{}{"event_id": "x"}), want: "wrap"},
		{name: "b envelope with wrong field type", in: dlqRec("2401.00001", map[string]interface{}{
			"event_id": testID, "event_type": testTarget, "paper_id": float64(7)}), want: "wrap"},
		{name: "c nil value (B2)", in: dlqRec("2401.00001", nil), want: "skip", reason: skipNilValue},
		{name: "c empty object", in: dlqRec("2401.00001", map[string]interface{}{}), want: "skip", reason: skipEmptyValue},
		{name: "c raw_value only", in: dlqRec("2401.00001", map[string]interface{}{"raw_value": "b'\\xff\\x00'"}),
			want: "skip", reason: skipRawValue},
		{name: "c raw object, empty key", in: dlqRec("", raw), want: "skip", reason: skipNoKey},
		{name: "c partial envelope, empty key", in: dlqRec("", map[string]interface{}{"event_id": "x"}), want: "skip", reason: skipNoKey},
	}
}

func TestReplayEnvelope(t *testing.T) {
	for _, tc := range envelopeCases() {
		t.Run(tc.name, func(t *testing.T) {
			env, reason, ok := replayEnvelope(tc.in, testTarget)
			switch tc.want {
			case "keep":
				if !ok || reason != "" {
					t.Fatalf("ok=%v reason=%q, want the original envelope", ok, reason)
				}
				if env.EventID != testID || env.EventType != testTarget || env.PaperID != "2401.00001" {
					t.Errorf("envelope = %+v, want the original event_id/event_type/paper_id", env)
				}
				if env.Attempt != 0 || env.Traceparent != testTrace {
					t.Errorf("attempt=%d traceparent=%q, want 0 and the DLQ payload's", env.Attempt, env.Traceparent)
				}
			case "wrap":
				if !ok || reason != "" {
					t.Fatalf("ok=%v reason=%q, want a new envelope", ok, reason)
				}
				if _, err := ulid.Parse(env.EventID); err != nil {
					t.Errorf("event_id %q is not a ULID: %v", env.EventID, err)
				}
				if env.EventType != testTarget || env.PaperID != tc.in.OriginalKey || env.Version != 1 || env.Attempt != 0 {
					t.Errorf("envelope = %+v, want event_type=%s paper_id=%s version=1 attempt=0", env, testTarget, tc.in.OriginalKey)
				}
				if !reflect.DeepEqual(env.Payload, tc.in.OriginalValue) {
					t.Errorf("payload = %v, want %v", env.Payload, tc.in.OriginalValue)
				}
				if env.Traceparent != testTrace {
					t.Errorf("traceparent = %q, want the DLQ payload's", env.Traceparent)
				}
			case "skip":
				if ok || reason != tc.reason {
					t.Fatalf("ok=%v reason=%q, want skipped with %q", ok, reason, tc.reason)
				}
				if !reflect.DeepEqual(env, events.EventEnvelope{}) {
					t.Errorf("skipped record returned envelope %+v, want none", env)
				}
			}
		})
	}
}

// AC2: whatever the input, an envelope the helper returns never has an empty ID field.
func TestReplayEnvelopeNeverEmpty(t *testing.T) {
	for _, tc := range envelopeCases() {
		env, _, ok := replayEnvelope(tc.in, testTarget)
		if ok && (env.EventID == "" || env.EventType == "" || env.PaperID == "") {
			t.Errorf("%s: returned envelope with an empty field: event_id=%q event_type=%q paper_id=%q",
				tc.name, env.EventID, env.EventType, env.PaperID)
		}
	}
}

func TestReplayEnvelopeSkipReasonsDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range []string{skipNilValue, skipEmptyValue, skipRawValue, skipNoKey} {
		if r == "" || seen[r] {
			t.Errorf("skip reason %q is empty or repeated", r)
		}
		seen[r] = true
	}
}

// AC3: the skip log line names the key, the error type and the reason.
func TestReplaySkipLine(t *testing.T) {
	line := skipLine(dlqRec("2401.00001", nil), skipNilValue)
	for _, want := range []string{`key="2401.00001"`, `error_type="ValueError"`, skipNilValue} {
		if !strings.Contains(line, want) {
			t.Errorf("skip line %q does not contain %q", line, want)
		}
	}
}
