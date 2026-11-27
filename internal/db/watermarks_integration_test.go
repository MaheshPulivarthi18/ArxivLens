//go:build integration

// Real-PostgreSQL checks of the crawl watermark SQL (ING-6a) and the crawl-loop lock (ING-6c); they apply
// migration 000005 themselves (IF NOT EXISTS). Run by .github/workflows/db.yml:
//
//	DATABASE_URL=postgres://... go test -tags integration -v -run '^(TestAdvanceWatermarkMonotonic|TestCrawlLoopLock)$' ./internal/db/
package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// connectIT connects to DATABASE_URL (skips without it) and closes the pool in a cleanup registered first, so it
// runs last (cleanups are LIFO) and every later cleanup still has a pool.
func connectIT(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	d, err := Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { // bounded: pgxpool's Close waits for acquired connections, so a leaked lock conn would hang the job
		done := make(chan struct{})
		go func() { d.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("DB.Close still blocked after 10s: a pooled connection was never released (crawl-loop lock?)")
		}
	})
	if err := d.RunMigrations(context.Background(), filepath.Join("..", "..", "migrations", "000005_crawl_watermarks.up.sql")); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAdvanceWatermarkMonotonic(t *testing.T) {
	ctx := context.Background()
	d := connectIT(t)
	cat := fmt.Sprintf("it-test.W%d", time.Now().UnixNano()) // no crawler uses it
	t.Cleanup(func() {
		if _, err := d.Pool.Exec(ctx, `DELETE FROM crawl_watermarks WHERE category = $1`, cat); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})

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

	// Errors are errors (design §1: a read error is never "no watermark"; QA mutants M6, M8).
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got, ok, err := d.Watermark(cancelled, cat); err == nil || ok || !got.IsZero() {
		t.Errorf("Watermark(cancelled ctx) = (%v, %v, %v), want (zero, false, error)", got, ok, err)
	}
	if err := d.AdvanceWatermark(cancelled, cat, t3.Add(time.Hour)); err == nil {
		t.Error("AdvanceWatermark(cancelled ctx) = nil, want an error")
	}

	// A conflicting advance (even one GREATEST ignores) bumps updated_at (M7).
	var before, after time.Time
	upd := `SELECT updated_at FROM crawl_watermarks WHERE category = $1`
	if err := d.Pool.QueryRow(ctx, upd, cat).Scan(&before); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // now() is the transaction start time: a later transaction is later
	if err := d.AdvanceWatermark(ctx, cat, t1); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool.QueryRow(ctx, upd, cat).Scan(&after); err != nil || !after.After(before) {
		t.Errorf("updated_at %v -> %v (err %v), want it to move forward", before, after, err)
	}
	if got, _, err := d.Watermark(ctx, cat); err != nil || !got.Equal(t3) {
		t.Errorf("after advancing to t1: Watermark = %v, %v; want t3 %v", got, err, t3)
	}
}

// TestCrawlLoopLock: one crawl-loop per database (ING-6c AC2). Two pools stand for two processes.
func TestCrawlLoopLock(t *testing.T) {
	ctx := context.Background()
	a, b := connectIT(t), connectIT(t)
	// held[i] is a lock connection still out of b's or a's pool. The cleanup (it runs before the pools close, LIFO)
	// releases it, so a failing check below reports its message instead of hanging in DB.Close.
	var held [2]func()
	t.Cleanup(func() {
		for _, r := range held {
			if r != nil {
				r()
			}
		}
	})
	var ok bool
	var err error
	if held[0], ok, err = a.TryCrawlLoopLock(ctx); err != nil || !ok || held[0] == nil {
		t.Fatalf("first acquirer: (release %v, ok %v, err %v), want the lock", held[0] != nil, ok, err)
	}
	if held[1], ok, err = b.TryCrawlLoopLock(ctx); err != nil || ok || held[1] != nil {
		t.Fatalf("second acquirer while held: (release %v, ok %v, err %v), want ok=false and nothing held", held[1] != nil, ok, err)
	}
	rel := held[0]
	held[0] = nil // the cleanup must not release twice
	rel()
	if held[1], ok, err = b.TryCrawlLoopLock(ctx); err != nil || !ok || held[1] == nil {
		t.Fatalf("second acquirer after release: (release %v, ok %v, err %v), want the lock", held[1] != nil, ok, err)
	}
	rel, held[1] = held[1], nil
	rel()
}
