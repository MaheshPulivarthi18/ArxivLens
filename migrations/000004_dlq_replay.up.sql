-- OPS-H7b (B-PIPE): resumable `arxivlens dlq replay` (design OPS-H7 Q1). Every offset of (dlq_topic, partition) below
-- next_offset is resolved, so a run starts there. dlq_replay_log has one row per resolved DLQ record: replayed (published
-- to target_topic as event_id, after the broker acked) or unreplayable. A record an --error-type filter skipped has no row
-- and holds the checkpoint at its offset, so it stays eligible. Reset a DLQ: DELETE FROM both tables WHERE dlq_topic = $1.
-- IF NOT EXISTS: `arxivlens migrate` re-applies every *.up.sql in order.
CREATE TABLE IF NOT EXISTS dlq_replay_checkpoints (
    dlq_topic   TEXT NOT NULL,
    partition   INT NOT NULL,
    next_offset BIGINT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (dlq_topic, partition)
);

CREATE TABLE IF NOT EXISTS dlq_replay_log (
    dlq_topic    TEXT NOT NULL,
    partition    INT NOT NULL,
    dlq_offset   BIGINT NOT NULL,
    outcome      TEXT NOT NULL CHECK (outcome IN ('replayed', 'unreplayable')),
    target_topic TEXT,
    event_id     TEXT,
    resolved_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (dlq_topic, partition, dlq_offset)
);
