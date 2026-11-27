package dlq

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is the Postgres Store (migration 000004_dlq_replay; design OPS-H7 Q1).
type PGStore struct{ pool *pgxpool.Pool }

var _ Store = (*PGStore)(nil)

func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

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
