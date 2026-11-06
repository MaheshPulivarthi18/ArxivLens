"""Worker heartbeat behind the compose worker healthcheck (OPS-H1). No Kafka needed: Consumer is faked."""
import os

import pytest

from services.common import kafka_client


class FakeConsumer:
    def __init__(self, conf):
        self.assigned = []

    def subscribe(self, topics):
        pass

    def assignment(self):
        return self.assigned


@pytest.fixture
def make_consumer(monkeypatch):
    monkeypatch.setattr(kafka_client, "Consumer", FakeConsumer)
    monkeypatch.setattr(kafka_client, "ServiceKafkaProducer", lambda brokers: None)

    def make(heartbeat_file):
        if heartbeat_file is None:
            monkeypatch.delenv("WORKER_HEARTBEAT_FILE", raising=False)
        else:
            monkeypatch.setenv("WORKER_HEARTBEAT_FILE", str(heartbeat_file))
        return kafka_client.ServiceKafkaConsumer(group_id="g", topics=["t"], brokers="unused:9092")

    return make


@pytest.mark.parametrize(
    "env_set, assigned, want_file",
    [
        (True, [("t", 0)], True),   # member with partitions: heartbeat written
        (True, [], False),          # subscribed but not (yet) in the group: no heartbeat
        (False, [("t", 0)], False), # host runs and tests: feature off
    ],
)
def test_heartbeat_only_while_assigned(tmp_path, make_consumer, env_set, assigned, want_file):
    hb = tmp_path / "hb"
    c = make_consumer(hb if env_set else None)
    c.consumer.assigned = assigned
    c._heartbeat()
    assert hb.exists() == want_file


def test_heartbeat_refreshes_mtime(tmp_path, make_consumer):
    hb = tmp_path / "hb"
    c = make_consumer(hb)
    c.consumer.assigned = [("t", 0)]
    c._heartbeat()
    os.utime(hb, (0, 0))
    c._heartbeat()
    assert hb.stat().st_mtime > 0


def test_stale_heartbeat_removed_on_start(tmp_path, make_consumer):
    hb = tmp_path / "hb"
    hb.write_text("")
    make_consumer(hb)
    assert not hb.exists()


def test_heartbeat_error_does_not_raise(tmp_path, make_consumer):
    c = make_consumer(tmp_path / "missing-dir" / "hb")
    c.consumer.assigned = [("t", 0)]
    c._heartbeat()  # OSError is logged, consumption continues
