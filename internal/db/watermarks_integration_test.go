//go:build integration

// Real-PostgreSQL check of the crawl watermark SQL (ING-6a); applies migration 000005 itself (IF NOT EXISTS):
//
//	DATABASE_URL=postgres://... go test -tags integration -v -run TestAdvanceWatermarkMonotonic ./internal/db/
package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAdvanceWatermarkMonotonic(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	d, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.RunMigrations(ctx, filepath.Join("..", "..", "migrations", "000005_crawl_watermarks.up.sql")); err != nil {
		t.Fatal(err)
	}
	cat := fmt.Sprintf("it-test.W%d", time.Now().UnixNano()) // no crawler uses it
	t.Cleanup(func() { _, _ = d.Pool.Exec(ctx, `DELETE FROM crawl_watermarks WHERE category = $1`, cat) })

	t1 := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	t2, t3 := t1.Add(time.Hour), t1.Add(2*time.Hour)
	for _, s := range []struct {
		name          string
		advance, want time.Time // advance zero: read only
	}{
		{"no row: ok=false", time.Time{}, time.Time{}},
		{"advance to t2", t2, t2},
		{"t1 < t2 does not move it back", t1, t2},
		{"equal value keeps it", t2, t2},
		{"advance to t3 > t2", t3, t3},
	} {
		if !s.advance.IsZero() {
			if err := d.AdvanceWatermark(ctx, cat, s.advance); err != nil {
				t.Fatalf("%s: %v", s.name, err)
			}
		}
		got, ok, err := d.Watermark(ctx, cat)
		if err != nil || ok == s.want.IsZero() || !got.Equal(s.want) {
			t.Fatalf("%s: Watermark = (%v, %v, %v), want (%v, %v, nil)", s.name, got, ok, err, s.want, !s.want.IsZero())
		}
	}
}
