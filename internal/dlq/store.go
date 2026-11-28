package dlq

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is the Postgres Store (migration 000004_dlq_replay; design OPS-H7 Q1, Q4).
type PGStore struct{ pool *pgxpool.Pool }

var _ Store = (*PGStore)(nil)

func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// lockKeySQL is the per-DLQ-topic advisory lock key: replays of different DLQs run in parallel.
const lockKeySQL = `hashtextextended('arxivlens.dlq.replay/' || $1, 0)`

// lockHolderSQL finds the session holding the key (a bigint key is split into classid/objid, objsubid 1).
const lockHolderSQL = `
	SELECT a.pid, coalesce(host(a.client_addr), 'local socket'), a.backend_start
	FROM (SELECT ` + lockKeySQL + ` AS k) key, pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
	WHERE l.locktype = 'advisory' AND l.granted AND l.objsubid = 1
	  AND l.classid = ((key.k >> 32) & 4294967295)::oid AND l.objid = (key.k & 4294967295)::oid
	LIMIT 1`

// TryLock takes the session advisory lock on a connection taken out of the pool and kept until unlock, so the lock
// lives as long as the process and is released by Postgres when its session ends (crash, kill). Not acquired: holder
// names the other session, best effort.
func (s *PGStore) TryLock(ctx context.Context, dlqTopic string) (unlock func(), ok bool, holder string, err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, "", fmt.Errorf("replay lock: acquire connection: %w", err)
	}
	// Bounds how long a network-partitioned holder keeps the lock (design Q4); best effort.
	_, _ = conn.Exec(ctx, `SET tcp_keepalives_idle = 30`)
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(`+lockKeySQL+`)`, dlqTopic).Scan(&ok); err != nil || !ok {
		if err == nil {
			var pid int
			var addr string
			var since time.Time
			if conn.QueryRow(ctx, lockHolderSQL, dlqTopic).Scan(&pid, &addr, &since) == nil {
				holder = fmt.Sprintf("pid %d on %s, since %s", pid, addr, since.UTC().Format(time.RFC3339))
			}
		}
		conn.Release()
		if err != nil {
			return nil, false, "", fmt.Errorf("replay lock: %w", err)
		}
		return nil, false, holder, nil
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock(`+lockKeySQL+`)`, dlqTopic); err != nil {
			_ = conn.Conn().Close(ctx) // closing the session releases the lock; the pool drops the closed connection
		}
		conn.Release()
	}, true, "", nil
}

func (s *PGStore) Checkpoints(ctx context.Context, dlqTopic string) (map[int32]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT partition, next_offset FROM dlq_replay_checkpoints WHERE dlq_topic = $1`, dlqTopic)
	if err != nil {
		return nil, fmt.Errorf("read dlq_replay_checkpoints: %w", err)
	}
	out := map[int32]int64{}
	var p int32
	var next int64
	_, err = pgx.ForEachRow(rows, []any{&p, &next}, func() error { out[p] = next; return nil })
	if err != nil {
		return nil, fmt.Errorf("read dlq_replay_checkpoints: %w", err)
	}
	return out, nil
}

func (s *PGStore) Resolved(ctx context.Context, dlqTopic string, p int32, from int64) (map[int64]bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT dlq_offset FROM dlq_replay_log WHERE dlq_topic = $1 AND partition = $2 AND dlq_offset >= $3`, dlqTopic, p, from)
	if err != nil {
		return nil, fmt.Errorf("read dlq_replay_log: %w", err)
	}
	out := map[int64]bool{}
	var o int64
	if _, err = pgx.ForEachRow(rows, []any{&o}, func() error { out[o] = true; return nil }); err != nil {
		return nil, fmt.Errorf("read dlq_replay_log: %w", err)
	}
	return out, nil
}

// MarkResolved: the log row and the checkpoint move in one transaction; GREATEST never moves a checkpoint back.
func (s *PGStore) MarkResolved(ctx context.Context, r Resolution) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if r.Outcome != "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO dlq_replay_log (dlq_topic, partition, dlq_offset, outcome, target_topic, event_id)
				VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''))
				ON CONFLICT (dlq_topic, partition, dlq_offset) DO NOTHING`,
				r.DLQTopic, r.Partition, r.Offset, r.Outcome, r.TargetTopic, r.EventID); err != nil {
				return fmt.Errorf("write dlq_replay_log: %w", err)
			}
		}
		if r.Next > 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO dlq_replay_checkpoints (dlq_topic, partition, next_offset) VALUES ($1, $2, $3)
				ON CONFLICT (dlq_topic, partition) DO UPDATE SET
					next_offset = GREATEST(dlq_replay_checkpoints.next_offset, EXCLUDED.next_offset), updated_at = now()`,
				r.DLQTopic, r.Partition, r.Next); err != nil {
				return fmt.Errorf("write dlq_replay_checkpoints: %w", err)
			}
		}
		return nil
	})
}
