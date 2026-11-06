import json
import logging
import os
import time
import traceback
from datetime import datetime, timezone
from typing import Callable, Dict, Any, List, Optional
from confluent_kafka import Consumer, Producer, Message, KafkaError
import ulid

from services.common.config import config
from shared.events.events import EventEnvelope, DLQPayload

logger = logging.getLogger(__name__)


class ServiceKafkaProducer:
    def __init__(self, brokers: str = None):
        self.brokers = brokers or config.kafka_brokers
        self.producer = Producer({
            "bootstrap.servers": self.brokers,
            "client.id": "arxivlens-python-producer",
            "acks": "all",
            "message.max.bytes": 1048576,
        })

    def publish(
        self,
        topic: str,
        event_type: str,
        paper_id: str,
        version: int,
        payload: Dict[str, Any],
        traceparent: Optional[str] = None,
    ) -> str:
        event_id = str(ulid.new())
        envelope = EventEnvelope(
            event_id=event_id,
            event_type=event_type,
            occurred_at=datetime.now(timezone.utc).isoformat(),
            paper_id=paper_id,
            version=version,
            attempt=0,
            traceparent=traceparent,
            payload=payload,
        )

        headers = []
        if traceparent:
            headers.append(("traceparent", traceparent.encode("utf-8")))

        val_bytes = json.dumps(envelope.model_dump()).encode("utf-8")
        key_bytes = paper_id.encode("utf-8")

        self.producer.produce(
            topic=topic,
            key=key_bytes,
            value=val_bytes,
            headers=headers,
        )
        self.producer.flush(timeout=5)
        logger.info(f"Published event {event_id} ({event_type}) to {topic} key={paper_id}")
        return event_id

    def publish_dlq(
        self,
        dlq_topic: str,
        original_msg: Optional[Message],
        error: Exception,
        consumer_group: str,
        attempts: int = 3,
        traceparent: Optional[str] = None,
        original_key: Optional[str] = None,
        original_value: Optional[Dict[str, Any]] = None,
    ) -> None:
        if original_msg is not None:
            try:
                orig_val = json.loads(original_msg.value().decode("utf-8"))
            except Exception:
                orig_val = {"raw_value": str(original_msg.value())}
            key_str = original_msg.key().decode("utf-8") if original_msg.key() else "unknown"
            orig_topic = original_msg.topic()
            orig_partition = original_msg.partition()
            orig_offset = original_msg.offset()
        else:
            orig_val = original_value or {}
            key_str = original_key or "unknown"
            orig_topic = dlq_topic.replace(".dlq", "")
            orig_partition = 0
            orig_offset = 0

        dlq_payload = DLQPayload(
            original_topic=orig_topic,
            partition=orig_partition,
            offset=orig_offset,
            original_key=key_str,
            original_value=orig_val,
            error_type=type(error).__name__,
            error_message=str(error),
            stack_trace=traceback.format_exc(),
            attempts=attempts,
            consumer_group=consumer_group,
            failed_at=datetime.now(timezone.utc).isoformat(),
            traceparent=traceparent,
        )

        headers = []
        if traceparent:
            headers.append(("traceparent", traceparent.encode("utf-8")))

        self.producer.produce(
            topic=dlq_topic,
            key=key_str.encode("utf-8"),
            value=json.dumps(dlq_payload.model_dump()).encode("utf-8"),
            headers=headers,
        )
        self.producer.flush(timeout=5)
        logger.warning(f"Routed failed message {key_str} to DLQ topic {dlq_topic}")


class ServiceKafkaConsumer:
    def __init__(
        self,
        group_id: str,
        topics: List[str],
        brokers: str = None,
        max_poll_records: int = 1,
    ):
        self.group_id = group_id
        self.topics = topics
        self.brokers = brokers or config.kafka_brokers
        self.producer = ServiceKafkaProducer(self.brokers)

        # §6 Rebalance Trap Fix: max.poll.records=1, enable.auto.commit=False
        self.consumer = Consumer({
            "bootstrap.servers": self.brokers,
            "group.id": self.group_id,
            "auto.offset.reset": "earliest",
            "enable.auto.commit": False,
            "max.poll.interval.ms": 300000,
        })
        self.consumer.subscribe(self.topics)
        # Worker-local health signal (compose healthcheck): touched after every poll while this consumer holds
        # partitions. A stale file left by an earlier run of this container is removed first. Unset: no file.
        self.heartbeat_file = os.environ.get("WORKER_HEARTBEAT_FILE")
        if self.heartbeat_file and os.path.exists(self.heartbeat_file):
            os.remove(self.heartbeat_file)

    def _heartbeat(self) -> None:
        if self.heartbeat_file and self.consumer.assignment():
            try:
                with open(self.heartbeat_file, "a"):
                    os.utime(self.heartbeat_file)
            except OSError as e:  # never let the health signal stop consumption
                logger.warning(f"heartbeat {self.heartbeat_file}: {e}")

    def start(self, handler: Callable[[EventEnvelope, Message], None]) -> None:
        logger.info(f"Starting consumer group={self.group_id} topics={self.topics}")
        try:
            while True:
                msg = self.consumer.poll(timeout=1.0)
                self._heartbeat()
                if msg is None:
                    continue
                if msg.error():
                    if msg.error().code() == KafkaError._PARTITION_EOF:
                        continue
                    logger.error(f"Kafka error: {msg.error()}")
                    continue

                # Extract traceparent from headers
                traceparent = None
                if msg.headers():
                    for k, v in msg.headers():
                        if k == "traceparent" and v:
                            traceparent = v.decode("utf-8")

                envelope_dict = json.loads(msg.value().decode("utf-8"))
                envelope = EventEnvelope(**envelope_dict)
                if traceparent:
                    envelope.traceparent = traceparent

                # Process message with bounded in-process retry (§6 Tier 1)
                max_attempts = 3
                success = False
                last_err = None

                for attempt in range(1, max_attempts + 1):
                    try:
                        handler(envelope, msg)
                        success = True
                        break
                    except Exception as e:
                        last_err = e
                        logger.warning(
                            f"Error in handler (attempt {attempt}/{max_attempts}) for key={envelope.paper_id}: {e}"
                        )
                        if attempt < max_attempts:
                            time.sleep(0.1 * (2**attempt))

                if not success and last_err:
                    # Route to DLQ (§6 Tier 2)
                    dlq_topic = f"{msg.topic()}.dlq"
                    self.producer.publish_dlq(
                        dlq_topic=dlq_topic,
                        original_msg=msg,
                        error=last_err,
                        consumer_group=self.group_id,
                        attempts=max_attempts,
                        traceparent=traceparent,
                    )

                # Commit offset only after DB transaction or DLQ routing succeeds (§4 Layer 4)
                self.consumer.commit(message=msg, asynchronous=False)

        except KeyboardInterrupt:
            logger.info("Consumer stopped by user.")
        finally:
            self.consumer.close()
