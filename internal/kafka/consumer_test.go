package kafka

import (
	"testing"
	"time"
)

func TestRetryBackoff(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 200 * time.Millisecond},
		{attempt: 2, want: 400 * time.Millisecond},
	}

	for _, tt := range tests {
		if got := retryBackoff(tt.attempt); got != tt.want {
			t.Errorf("retryBackoff(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

// TestRetrySleepSchedule mirrors the retry loop in Consumer.Start (attempt starts at 1,
// sleeps only while attempt < maxRetries) and pins the delays it uses with maxRetries = 3.
func TestRetrySleepSchedule(t *testing.T) {
	const maxRetries = 3

	var sleeps []time.Duration
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt < maxRetries {
			sleeps = append(sleeps, retryBackoff(attempt))
		}
	}

	want := []time.Duration{200 * time.Millisecond, 400 * time.Millisecond}
	if len(sleeps) != len(want) {
		t.Fatalf("got %d sleeps %v, want %v", len(sleeps), sleeps, want)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Errorf("sleep[%d] = %v, want %v", i, sleeps[i], want[i])
		}
	}
}
