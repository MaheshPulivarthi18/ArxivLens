"""OPS-H8: the Python consumer commits only after success or a confirmed DLQ write. Fakes only: no broker, no network."""
import json
import logging
import signal
import threading
import time

import pytest
from confluent_kafka import KafkaException

from services.common import kafka_client as kc

TP = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
TOPIC, DLQ = "papers.downloaded", "papers.downloaded.dlq"


def envelope(offset, marker=""):
    return json.dumps({"event_id": f"e{offset}", "event_type": "paper.downloaded", "occurred_at": "2026-11-07T00:00:00Z",
                       "paper_id": "2401.00001", "payload": {"note": marker}}).encode()


class FakeMsg:
    def __init__(self, offset, value=None, headers=(("traceparent", TP.encode()),)):
        self._offset, self._value, self._headers = offset, envelope(offset) if value is None else value, list(headers)

    def topic(self): return TOPIC
    def partition(self): return 0
    def offset(self): return self._offset
    def key(self): return b"2401.00001"
    def value(self): return self._value
    def headers(self): return self._headers
    def error(self): return None


class FakeConsumer:
    """poll() pops the script; once empty it asks the owner to stop. commit/close append to the shared log."""

    def __init__(self, script, log, commit_errors=()):
        self.script, self.log, self.commit_errors, self.owner = list(script), log, set(commit_errors), None

    def subscribe(self, topics): pass
    def assignment(self): return [(TOPIC, 0)]

    def poll(self, timeout):
        if not self.script:
            self.owner.stop()
            return None
        return self.script.pop(0)

    def commit(self, message, asynchronous):
        if message.offset() in self.commit_errors:
            raise KafkaException("Broker: Unknown member")
        self.log.append(("commit", message.offset()))

    def close(self): self.log.append(("close",))


class FakeErr:
    def __str__(self): return "Local: Message timed out"


class FakeProducer:
    """outcomes: per produce, 'ok' | 'err' (callback with error) | 'queued' (no callback) | 'raise' (produce raises).
    The last outcome repeats."""

    def __init__(self, outcomes, log=None):
        self.outcomes, self.log, self.sent, self.pending = list(outcomes), [] if log is None else log, [], None

    def produce(self, topic, key, value, headers, on_delivery):
        outcome = self.outcomes.pop(0) if len(self.outcomes) > 1 else self.outcomes[0]
        if outcome == "raise":
            raise BufferError("Local: Queue full")
        body = json.loads(value)
        self.log.append(("produce", topic, body.get("offset")))
        self.pending = (outcome, on_delivery, body, headers)

    def flush(self, timeout):
        outcome, cb, body, headers = self.pending
        if outcome == "queued":
            return 1
        cb(None if outcome == "ok" else FakeErr(), None)
        if outcome == "ok":
            self.sent.append((body, headers))
        return 0


def make(script, dlq=("ok",), commit_errors=(), stop_after_waits=None):
    log, waits = [], []
    c = kc.ServiceKafkaConsumer("processor-v1", [TOPIC], brokers="unused:9092",
                                consumer=FakeConsumer(script, log, commit_errors),
                                producer=kc.ServiceKafkaProducer("unused:9092", producer=FakeProducer(dlq, log)))
    c.consumer.owner = c

    def fake_wait(seconds):
        waits.append(seconds)
        if stop_after_waits is not None and len(waits) >= stop_after_waits:
            c.stop()
        return c._stop.is_set()

    c._wait = fake_wait
    return c, log, waits


def handler_failing(fail_counts, calls, exc=RuntimeError("boom")):
    """fail_counts: offset -> number of leading failures (99 = always)."""

    def handle(env, msg):
        calls[msg.offset()] = calls.get(msg.offset(), 0) + 1
        if calls[msg.offset()] <= fail_counts.get(msg.offset(), 0):
            raise exc

    return handle


def always_failing(calls, exc=None):
    return handler_failing({5: 99}, calls, exc or RuntimeError("boom"))


# AC1: success on attempt 1, 2 or 3 -> exactly one commit, no DLQ produce
@pytest.mark.parametrize("failures, want_waits", [(0, []), (1, [0.2]), (2, [0.2, 0.4])])
def test_success_path(failures, want_waits):
    calls = {}
    c, log, waits = make([FakeMsg(5)])
    c.start(handler_failing({5: failures}, calls))
    assert log == [("commit", 5), ("close",)]
    assert waits == pytest.approx(want_waits) and calls == {5: failures + 1}


def test_commit_error_is_logged_and_loop_continues(caplog):
    c, log, _ = make([FakeMsg(5), FakeMsg(6)], commit_errors={5})
    c.start(handler_failing({}, {}))
    assert log == [("commit", 6), ("close",)]
    assert any("Commit failed (topic=papers.downloaded partition=0 offset=5)" in r.getMessage() for r in caplog.records)


# AC2: 3 failures -> one DLQ message with full diagnostics, committed only after the confirmed write
def test_dlq_then_commit():
    calls = {}
    c, log, waits = make([FakeMsg(5)])
    c.start(always_failing(calls))
    assert log == [("produce", DLQ, 5), ("commit", 5), ("close",)]
    assert calls == {5: 3} and waits == pytest.approx([0.2, 0.4])
    (body, headers), = c.producer.producer.sent
    assert (body["original_topic"], body["partition"], body["offset"], body["attempts"]) == (TOPIC, 0, 5, 3)
    assert (body["consumer_group"], body["traceparent"], headers) == ("processor-v1", TP, [("traceparent", TP.encode())])
    assert "in handle" in body["stack_trace"] and "RuntimeError: boom" in body["stack_trace"]  # the handler's trace


# AC3: the DLQ write never lands -> nothing at or after @5 is committed, the handler isn't re-run
@pytest.mark.parametrize("outcome", ["err", "queued"])
def test_dlq_failure_blocks_and_never_commits(outcome):
    calls = {}
    c, log, waits = make([FakeMsg(4), FakeMsg(5), FakeMsg(6)], dlq=[outcome], stop_after_waits=5)
    c.start(handler_failing({5: 99}, calls))
    assert ("commit", 4) in log and ("commit", 5) not in log and ("commit", 6) not in log
    assert log.count(("produce", DLQ, 5)) >= 2 and calls == {4: 1, 5: 3}
    assert waits == pytest.approx([0.2, 0.4, 1, 2, 4]) and log[-1] == ("close",)


# AC4: two failed rounds, then the write lands -> one DLQ message, one commit, waits 1 s and 2 s
def test_dlq_recovers_after_two_failures():
    calls = {}
    c, log, waits = make([FakeMsg(5)], dlq=["err", "err", "ok"])
    c.start(always_failing(calls))
    assert log == [("produce", DLQ, 5)] * 3 + [("commit", 5), ("close",)]
    assert len(c.producer.producer.sent) == 1 and calls == {5: 3}
    assert waits == pytest.approx([0.2, 0.4, 1, 2])


def test_dlq_backoff_schedule():
    c, _, _ = make([])
    assert [c._dlq_backoff(r) for r in range(1, 8)] == [1, 2, 4, 8, 16, 30, 30]


# AC5: publish returns only on a confirmed delivery
@pytest.mark.parametrize("outcome", ["ok", "err", "queued", "raise"])
def test_publish_requires_confirmation(outcome):
    p = kc.ServiceKafkaProducer("unused:9092", producer=FakeProducer([outcome]))
    if outcome == "ok":
        assert p.publish("papers.chunked", "paper.chunked", "2401.00001", 1, {}, traceparent=TP)
    else:
        with pytest.raises(kc.DeliveryError):
            p.publish("papers.chunked", "paper.chunked", "2401.00001", 1, {}, traceparent=TP)


@pytest.mark.parametrize("dlq", ["ok", "err"])
def test_failing_publish_in_handler_is_retried_not_committed(dlq):
    downstream, calls = kc.ServiceKafkaProducer("unused:9092", producer=FakeProducer(["err"])), []

    def processor_like(env, msg):
        calls.append(msg.offset())
        downstream.publish("papers.chunked", "paper.chunked", env.paper_id, 1, {}, traceparent=env.traceparent)

    c, log, _ = make([FakeMsg(5)], dlq=[dlq], stop_after_waits=4)
    c.start(processor_like)
    assert calls == [5, 5, 5]
    if dlq == "ok":
        assert log == [("produce", DLQ, 5), ("commit", 5), ("close",)]
        assert c.producer.producer.sent[0][0]["error_type"] == "DeliveryError"
    else:
        assert not [e for e in log if e[0] == "commit"]


def test_delivery_bound_outlasts_message_timeout(monkeypatch):
    assert kc.DELIVERY_TIMEOUT_S > kc.MESSAGE_TIMEOUT_MS / 1000
    monkeypatch.setattr(kc, "Producer", lambda conf: conf)
    p = kc.ServiceKafkaProducer("unused:9092")
    assert p.producer["message.timeout.ms"] == kc.MESSAGE_TIMEOUT_MS and p.delivery_timeout_s == kc.DELIVERY_TIMEOUT_S


# AC6: a stop interrupts a 5 s wait at once: no commit, no (further) DLQ write
@pytest.mark.parametrize("stage", ["handler-wait", "dlq-wait"])
def test_stop_during_wait_returns_promptly(stage):
    c, log, _ = make([FakeMsg(5)], dlq=["err"])
    c._wait = c._stop.wait  # real interruptible wait
    c._handler_backoff = (lambda a: 5.0) if stage == "handler-wait" else (lambda a: 0)
    c._dlq_backoff = lambda r: 5.0
    threading.Timer(0.05, c.stop).start()
    t0 = time.monotonic()
    c.start(always_failing({}))
    assert time.monotonic() - t0 < 0.2
    assert log == ([] if stage == "handler-wait" else [("produce", DLQ, 5)]) + [("close",)]


# AC6b: the handler fails because shutdown was requested -> not a processing failure
@pytest.mark.parametrize("stop_on_call, want_waits", [(3, [0.2, 0.4]), (1, [])])
def test_stop_during_last_attempt_is_not_a_failure(stop_on_call, want_waits):
    calls = []
    c, log, waits = make([FakeMsg(5)])

    def handler(env, msg):
        calls.append(1)
        if len(calls) == stop_on_call:
            c.stop()
        raise RuntimeError("interrupted")

    c.start(handler)
    assert len(calls) == stop_on_call and waits == pytest.approx(want_waits) and log == [("close",)]


def test_signal_handler_sets_stop_and_is_restored():
    before, calls = signal.getsignal(signal.SIGTERM), []
    c, log, _ = make([FakeMsg(5), FakeMsg(6)])

    def handler(env, msg):
        calls.append(msg.offset())
        signal.getsignal(signal.SIGTERM)(signal.SIGTERM, None)  # what `docker stop` delivers

    c.start(handler)
    assert calls == [5] and log == [("commit", 5), ("close",)]  # finished record committed, then exit
    assert signal.getsignal(signal.SIGTERM) is before
    with pytest.raises(KeyboardInterrupt):  # a second signal forces the stop
        c._on_signal(signal.SIGINT, None)


# AC7: an undecodable record goes to the DLQ once (attempts=1), is committed, and the loop keeps consuming
@pytest.mark.parametrize("value, want", [
    (b"not json", {"raw_value": "b'not json'"}),
    (b'{"event_id": "x"}', {"event_id": "x"}),
    (b"[1, 2]", {"raw_value": "b'[1, 2]'"}),
])
def test_poison_record_goes_to_dlq_and_loop_continues(value, want):
    calls = {}
    c, log, _ = make([FakeMsg(5, value=value), FakeMsg(6)])
    c.start(handler_failing({}, calls))
    assert log == [("produce", DLQ, 5), ("commit", 5), ("commit", 6), ("close",)] and calls == {6: 1}
    body = c.producer.producer.sent[0][0]
    assert (body["offset"], body["attempts"], body["original_value"]) == (5, 1, want)


# AC8: a failed DLQ write is logged with coordinates and the error, never with the record value
@pytest.mark.parametrize("poison", [False, True])
def test_dlq_failure_log_has_coordinates_not_value(caplog, poison):
    marker = "SENTINEL-7f3a"
    value = json.dumps({"event_id": marker}).encode() if poison else envelope(5, marker)
    c, log, _ = make([FakeMsg(5, value=value)], dlq=["err", "ok"])
    caplog.set_level(logging.DEBUG)
    c.start(always_failing({}, RuntimeError(f"bad value {marker}")))
    crit = [r.getMessage() for r in caplog.records if r.levelno == logging.CRITICAL]
    assert len(crit) == 1 and ("commit", 5) in log
    for part in ["topic=papers.downloaded", "partition=0", "offset=5", f"dlq_topic={DLQ}", "round=1", "Message timed out"]:
        assert part in crit[0]
    assert not [r for r in caplog.records if marker in r.getMessage()]


# AC9: M4's direct call keeps its signature and contract (returns on success, raises only on a failed delivery)
@pytest.mark.parametrize("outcome", ["ok", "err"])
def test_publish_dlq_direct_call_contract(outcome):
    fake = FakeProducer([outcome])
    p = kc.ServiceKafkaProducer("unused:9092", producer=fake)
    try:
        raise ValueError("corrupt envelope")
    except ValueError as e:
        err = e
    kwargs = dict(dlq_topic=DLQ, original_msg=None, error=err, consumer_group="processor-v1", attempts=3, traceparent=TP)
    if outcome == "err":
        with pytest.raises(kc.DeliveryError):
            p.publish_dlq(**kwargs)
        return
    assert p.publish_dlq(**kwargs) is None
    body = fake.sent[0][0]
    assert (body["original_topic"], body["original_value"], body["original_key"]) == (TOPIC, {}, "unknown")
    assert "ValueError: corrupt envelope" in body["stack_trace"]


def test_build_dlq_payload_is_total():
    msg = FakeMsg(5, value=b'"just a string"')
    msg.key = lambda: b"\xff\xfe"
    body = kc.ServiceKafkaProducer("unused:9092", producer=FakeProducer(["ok"])).build_dlq_payload(
        DLQ, msg, RuntimeError("x"), "processor-v1")
    assert body.original_value == {"raw_value": "b'\"just a string\"'"} and body.original_key == "\ufffd\ufffd"
