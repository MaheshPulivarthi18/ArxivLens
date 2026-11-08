// Package arxiv holds the politeness rules every Go component follows when it calls arXiv
// (Technical PRD §9 "External politeness"): at most one request per DefaultInterval and an
// identifying User-Agent on each request.
//
// The guarantee is per process: each Limiter spaces the requests made through it. The crawler,
// downloader and query service are separate processes and are not coordinated with each other.
package arxiv

import (
	"context"
	"time"
)

// DefaultInterval is the minimum gap between the starts of two arXiv requests in one process.
// It is a constant on purpose: no env var or flag may lower it.
const DefaultInterval = 3 * time.Second

// UserAgent returns the identifying User-Agent for an arXiv request made by component, e.g.
// "ArxivLens/1.0 (Query Ingester; mailto:contact@arxivlens.local)". The address is a placeholder.
func UserAgent(component string) string {
	return "ArxivLens/1.0 (" + component + "; mailto:contact@arxivlens.local)"
}

// Limiter hands out permits at least interval apart. It is safe for concurrent use; callers are
// served one at a time.
type Limiter struct {
	interval time.Duration
	turn     chan struct{} // capacity 1: holding it means "my turn"
	last     time.Time     // time of the previous permit; guarded by turn
}

// NewLimiter returns a Limiter whose permits are at least interval apart.
func NewLimiter(interval time.Duration) *Limiter {
	return &Limiter{interval: interval, turn: make(chan struct{}, 1)}
}

// Interval reports the minimum gap between permits.
func (l *Limiter) Interval() time.Duration { return l.interval }

// Wait blocks until at least interval has passed since the previous permit (the first call
// returns at once). If ctx ends first it returns ctx.Err() and does not use up a permit, so the
// next caller is not delayed by it.
func (l *Limiter) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case l.turn <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-l.turn }()

	if !l.last.IsZero() {
		if d := time.Until(l.last.Add(l.interval)); d > 0 {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	l.last = time.Now()
	return nil
}
