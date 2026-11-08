"""OPS-H8 (part a): the Python producer confirms delivery, so no offset is committed past an unconfirmed DLQ write.
Fakes only: no broker, no network. The consumer loop (`start`) is the unchanged one from main."""
import json

import pytest

from services.common import kafka_client as kc

TP = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
TOPIC, DLQ = "papers.downloaded", "papers.downloaded.dlq"
SENTINEL = "SENTINEL-7f3a"


def envelope(offset):
    return json.dumps({"event_id": f"e{offset}", "event_type": "paper.downloaded", "occurred_at": "2026-11-07T00:00:00Z",
                       "paper_id": "2401.00001", "payload": {"note": SENTINEL}}).encode()


class FakeMsg:
    def __init__(self, offset, value=None, headers=(("traceparent", TP.encode()),)):
        self._offset, self._value = offset, envelope(offset) if value is None else value
        self._headers = None if headers is None else list(headers)

    def topic(self): return TOPIC
    def partition(self): return 0
    def offset(self): return self._offset
    def key(self): return b"2401.00001"
    def value(self): return self._value
    def headers(self): return self._headers
    def error(self): return None


class FakeConsumer:
    """poll() pops the script; once it is empty, KeyboardInterrupt ends the unchanged `while True` loop."""

    def __init__(self, script, log):
        self.script, self.log = list(script), log

    def subscribe(self, topics): pass
    def assignment(self): return [(TOPIC, 0)]

    def poll(self, timeout):
        if not self.script:
            raise KeyboardInterrupt
        return self.script.pop(0)

    def commit(self, message, asynchronous):
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


@pytest.fixture
def sleeps(monkeypatch):
    calls = []
    monkeypatch.setattr(kc.time, "sleep", calls.append)
    return calls


def make(script, dlq=("ok",)):
    log = []
    c = kc.ServiceKafkaConsumer("processor-v1", [TOPIC], brokers="unused:9092", consumer=FakeConsumer(script, log),
                                producer=kc.ServiceKafkaProducer("unused:9092", producer=FakeProducer(dlq, log)))
    return c, log


def failing_at(offset, calls):
    def handle(env, msg):
        calls[msg.offset()] = calls.get(msg.offset(), 0) + 1
        if msg.offset() == offset:
            raise RuntimeError("boom")
    return handle


# AC1: publish returns only on a confirmed delivery; the error names topic and cause, never the record value
@pytest.mark.parametrize("outcome, cause", [("ok", None), ("err", "Message timed out"), ("queued", "not confirmed"),
                                            ("raise", "Queue full")])
def test_publish_requires_confirmation(outcome, cause):
    p = kc.ServiceKafkaProducer("unused:9092", producer=FakeProducer([outcome]))
    args = ("papers.chunked", "paper.chunked", "2401.00001", 1, {"note": SENTINEL})
    if outcome == "ok":
        assert p.publish(*args, traceparent=TP)
        return
    with pytest.raises(kc.DeliveryError) as exc:
        p.publish(*args, traceparent=TP)
    assert "papers.chunked" in str(exc.value) and cause in str(exc.value) and SENTINEL not in str(exc.value)


# AC2: M4's direct call keeps its signature and fallbacks; returns on success, raises on an unconfirmed delivery
@pytest.mark.parametrize("outcome", ["ok", "err", "queued"])
def test_publish_dlq_direct_call_contract(outcome):
    fake = FakeProducer([outcome])
    p = kc.ServiceKafkaProducer("unused:9092", producer=fake)
    kwargs = dict(dlq_topic=DLQ, original_msg=None, error=ValueError("corrupt envelope"), consumer_group="processor-v1",
                  attempts=3, traceparent=TP)
    if outcome != "ok":
        with pytest.raises(kc.DeliveryError):
            p.publish_dlq(**kwargs)
        return
    assert p.publish_dlq(**kwargs) is None
    (body, headers), = fake.sent
    assert (body["original_topic"], body["original_value"], body["original_key"]) == (TOPIC, {}, "unknown")
    assert (body["attempts"], body["traceparent"], headers) == (3, TP, [("traceparent", TP.encode())])


@pytest.mark.parametrize("msg, want_value", [
    (None, {}),
    (FakeMsg(5, value=b"\xff\xfe not utf-8"), {"raw_value": "b'\\xff\\xfe not utf-8'"}),
    (FakeMsg(5, headers=None), json.loads(envelope(5))),
])
def test_build_dlq_payload_is_total(msg, want_value):
    p = kc.ServiceKafkaProducer("unused:9092", producer=FakeProducer(["ok"]))
    body = p.build_dlq_payload(DLQ, msg, RuntimeError("x"), "processor-v1")
    assert isinstance(body, kc.DLQPayload) and body.original_value == want_value and body.original_topic == TOPIC


# AC3: the stack trace is the error's own, even when built after its except block ended (the consumer path)
def test_dlq_stack_trace_is_the_errors_own():
    def parse_record_helper():
        raise ValueError("bad record")

    try:
        parse_record_helper()
    except ValueError as e:
        err = e
    body = kc.ServiceKafkaProducer("unused:9092", producer=FakeProducer(["ok"])).build_dlq_payload(DLQ, None, err, "g")
    assert "parse_record_helper" in body.stack_trace and "NoneType: None" not in body.stack_trace


# AC4: the confirmation wait outlasts librdkafka's own message timeout, which the default config sets
def test_delivery_bound_outlasts_message_timeout(monkeypatch):
    assert kc.DELIVERY_TIMEOUT_S > kc.MESSAGE_TIMEOUT_MS / 1000
    monkeypatch.setattr(kc, "Producer", lambda conf: conf)
    p = kc.ServiceKafkaProducer("unused:9092")
    assert p.producer["message.timeout.ms"] == kc.MESSAGE_TIMEOUT_MS and p.delivery_timeout_s == kc.DELIVERY_TIMEOUT_S


# AC5 (interim, unchanged start): an unconfirmed DLQ write leaves start before the commit; nothing after it is processed
@pytest.mark.parametrize("outcome", ["err", "queued"])
def test_unconfirmed_dlq_write_stops_before_commit(outcome, sleeps):
    calls = {}
    c, log = make([FakeMsg(4), FakeMsg(5), FakeMsg(6)], dlq=[outcome])
    with pytest.raises(kc.DeliveryError):
        c.start(failing_at(5, calls))
    commits = [e for e in log if e[0] == "commit"]
    assert commits == [("commit", 4)] and calls == {4: 1, 5: 3} and log[-1] == ("close",)


def test_confirmed_dlq_write_then_commit(sleeps):
    calls = {}
    c, log = make([FakeMsg(5)])
    c.start(failing_at(5, calls))
    assert log == [("produce", DLQ, 5), ("commit", 5), ("close",)] and calls == {5: 3} and sleeps == [0.2, 0.4]
    (body, headers), = c.producer.producer.sent
    assert (body["original_topic"], body["partition"], body["offset"], body["attempts"]) == (TOPIC, 0, 5, 3)
    assert body["consumer_group"] == "processor-v1" and "RuntimeError: boom" in body["stack_trace"]


def test_failing_publish_in_handler_is_retried(sleeps):
    downstream, calls = kc.ServiceKafkaProducer("unused:9092", producer=FakeProducer(["err"])), []

    def processor_like(env, msg):
        calls.append(msg.offset())
        downstream.publish("papers.chunked", "paper.chunked", env.paper_id, 1, {}, traceparent=env.traceparent)

    c, log = make([FakeMsg(5)])
    c.start(processor_like)
    assert calls == [5, 5, 5] and log == [("produce", DLQ, 5), ("commit", 5), ("close",)]
    assert c.producer.producer.sent[0][0]["error_type"] == "DeliveryError"
