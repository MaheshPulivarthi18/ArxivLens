package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// advanceWatermarkSQL moves a category's watermark forward only: GREATEST keeps the stored value when the new
// one is lower, so a retry, a stale process or a second writer can never move it back (ING-6 design §1).
const advanceWatermarkSQL = `
	INSERT INTO crawl_watermarks (category, last_published_at, updated_at)
	VALUES ($1, $2, now())
	ON CONFLICT (category) DO UPDATE SET
		last_published_at = GREATEST(crawl_watermarks.last_published_at, EXCLUDED.last_published_at),
		updated_at = now()`

// Watermark returns the crawl watermark of category. ok is false when the category has none yet (first run);
// a read error is returned as such and must never be treated as "no watermark".
func (d *DB) Watermark(ctx context.Context, category string) (time.Time, bool, error) {
	var t time.Time
	err := d.Pool.QueryRow(ctx, `SELECT last_published_at FROM crawl_watermarks WHERE category = $1`, category).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("failed to read crawl watermark for %s: %w", category, err)
	}
	return t, true, nil
}

// AdvanceWatermark sets category's watermark to t unless the stored one is already later.
func (d *DB) AdvanceWatermark(ctx context.Context, category string, t time.Time) error {
	if _, err := d.Pool.Exec(ctx, advanceWatermarkSQL, category, t); err != nil {
		return fmt.Errorf("failed to advance crawl watermark for %s: %w", category, err)
	}
	return nil
}

// crawlLoopLockKeySQL is the one global crawl-loop lock (ING-6 design, PM answer 3). It is not per category: the
// arXiv limiter is per process, so any second process would double the request rate to arXiv.
const crawlLoopLockKeySQL = `hashtext('arxivlens:crawl-loop')`

// TryCrawlLoopLock takes the session-level advisory lock of crawl-loop on a connection taken out of the pool
// and held until release, so the lock lives as long as the process (and dies with its session on a crash).
// ok is false, with nothing held, when another session has it.
func (d *DB) TryCrawlLoopLock(ctx context.Context) (release func(), ok bool, err error) {
	conn, err := d.Pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("crawl-loop lock: acquire connection: %w", err)
	}
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(`+crawlLoopLockKeySQL+`)`).Scan(&ok); err != nil || !ok {
		conn.Release()
		if err != nil {
			return nil, false, fmt.Errorf("crawl-loop lock: %w", err)
		}
		return nil, false, nil
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock(`+crawlLoopLockKeySQL+`)`); err != nil {
			_ = conn.Conn().Close(ctx) // closing the session releases the lock; the pool drops the closed connection
		}
		conn.Release()
	}, true, nil
}
