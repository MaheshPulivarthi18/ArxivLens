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
