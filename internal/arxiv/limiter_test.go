package arxiv

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"
)

// slack absorbs scheduler jitter between a permit and the caller recording its timestamp.
const slack = 20 * time.Millisecond

func TestDefaultsAndUserAgent(t *testing.T) {
	if DefaultInterval != 3*time.Second || NewLimiter(DefaultInterval).Interval() != 3*time.Second {
		t.Fatalf("DefaultInterval = %s, want 3s (Technical PRD §9)", DefaultInterval)
	}
	if got, want := UserAgent("Query Ingester"), "ArxivLens/1.0 (Query Ingester; mailto:contact@arxivlens.local)"; got != want {
		t.Errorf("UserAgent = %q, want %q", got, want)
	}
}

// waitImmediate calls Wait and fails unless it returns within slack.
func waitImmediate(t *testing.T, l *Limiter) {
	t.Helper()
	start := time.Now()
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if el := time.Since(start); el > slack {
		t.Fatalf("Wait took %s, want immediate", el)
	}
}

// The first Wait is immediate, and a call with an already-cancelled ctx uses no permit.
func TestFirstWaitIsImmediate(t *testing.T) {
	l := NewLimiter(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait(cancelled) = %v, want context.Canceled", err)
	}
	waitImmediate(t, l)
}

func TestSequentialWaits(t *testing.T) {
	const n, d = 5, 40 * time.Millisecond
	l := NewLimiter(d)
	start := time.Now()
	for i := 0; i < n; i++ {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatalf("Wait %d: %v", i, err)
		}
	}
	if el := time.Since(start); el < (n-1)*d {
		t.Fatalf("%d waits took %s, want >= %s", n, el, (n-1)*d)
	}
}

func TestConcurrentCallersAreSpaced(t *testing.T) {
	const n, d = 10, 30 * time.Millisecond
	l := NewLimiter(d)
	stamps := make([]time.Time, n)
	var wg sync.WaitGroup
	for i := range stamps {
		wg.Add(1)
		go func() { defer wg.Done(); _ = l.Wait(context.Background()); stamps[i] = time.Now() }()
	}
	wg.Wait()
	sort.Slice(stamps, func(i, j int) bool { return stamps[i].Before(stamps[j]) })
	for i := 1; i < n; i++ {
		if gap := stamps[i].Sub(stamps[i-1]); gap < d-slack {
			t.Errorf("gap %d = %s, want >= %s (slack %s)", i, gap, d, slack)
		}
	}
}

// A waiter cancelled while sleeping for its slot (queued=false) or while queued behind another
// caller (queued=true) returns context.Canceled at once, and the next permit is not delayed by it.
func TestCancelledWaiter(t *testing.T) {
	for _, queued := range []bool{false, true} {
		const d = 150 * time.Millisecond
		l := NewLimiter(d)
		waitImmediate(t, l)
		t0 := time.Now()
		next := make(chan time.Time, 1)
		if queued {
			go func() { _ = l.Wait(context.Background()); next <- time.Now() }()
			time.Sleep(20 * time.Millisecond) // let it take the turn
		}

		ctx, cancel := context.WithCancel(context.Background())
		var cancelledAt time.Time
		time.AfterFunc(20*time.Millisecond, func() { cancelledAt = time.Now(); cancel() })
		err := l.Wait(ctx)
		if lag := time.Since(cancelledAt); !errors.Is(err, context.Canceled) || lag > slack {
			t.Fatalf("queued=%v: Wait = %v after %s, want context.Canceled within %s", queued, err, lag, slack)
		}

		if !queued {
			go func() { _ = l.Wait(context.Background()); next <- time.Now() }()
		}
		if at := (<-next).Sub(t0); at < d-slack || at > d+slack {
			t.Fatalf("queued=%v: next permit at %s, want ~%s", queued, at, d)
		}
	}
}
