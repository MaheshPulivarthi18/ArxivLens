package config

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		name string
		val  string
		want time.Duration
	}{
		{name: "unset uses default", val: "", want: DefaultIngestTimeout},
		{name: "minutes", val: "10m", want: 10 * time.Minute},
		{name: "seconds", val: "90s", want: 90 * time.Second},
		{name: "bare number is invalid", val: "300", want: DefaultIngestTimeout},
		{name: "garbage", val: "five minutes", want: DefaultIngestTimeout},
		{name: "zero", val: "0s", want: DefaultIngestTimeout},
		{name: "negative", val: "-1m", want: DefaultIngestTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseDuration("INGEST_TIMEOUT", tt.val, DefaultIngestTimeout); got != tt.want {
				t.Fatalf("parseDuration(%q) = %s, want %s", tt.val, got, tt.want)
			}
		})
	}
}

func TestLoadConfigIngestTimeout(t *testing.T) {
	if DefaultIngestTimeout != 5*time.Minute {
		t.Fatalf("DefaultIngestTimeout = %s, want 5m", DefaultIngestTimeout)
	}
	tests := []struct {
		name string
		env  string
		want time.Duration
	}{
		{name: "default", env: "", want: 5 * time.Minute},
		{name: "override", env: "7m30s", want: 7*time.Minute + 30*time.Second},
		{name: "invalid falls back", env: "abc", want: 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("INGEST_TIMEOUT", tt.env)
			if got := LoadConfig().IngestTimeout; got != tt.want {
				t.Fatalf("LoadConfig().IngestTimeout = %s with INGEST_TIMEOUT=%q, want %s", got, tt.env, tt.want)
			}
		})
	}
}
