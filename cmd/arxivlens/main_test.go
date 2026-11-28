package main

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"arxivlens/internal/config"
	"arxivlens/internal/crawler"
)

// TestCrawlLoopFlags: bad flags fail before run (the only code that connects to Postgres, Kafka or arXiv) is called.
func TestCrawlLoopFlags(t *testing.T) {
	defaults := crawler.LoopConfig{Categories: []string{"cs.AI", "cs.LG"}, Interval: 6 * time.Hour, LimitPerRun: 100, MaxScan: 2000}
	for _, c := range []struct {
		name    string
		args    []string
		want    *crawler.LoopConfig // nil: rejected
		wantErr string
	}{
		{"defaults", []string{}, &defaults, ""}, // not nil: cobra would read os.Args
		{"valid overrides", []string{"--categories", "cs.LG", "--interval", "1m", "--limit-per-run", "2", "--max-scan", "2", "--once"},
			&crawler.LoopConfig{Categories: []string{"cs.LG"}, Interval: time.Minute, LimitPerRun: 2, MaxScan: 2, Once: true}, ""},
		{"interval 30s", []string{"--interval", "30s"}, nil, "--interval 30s: must be at least 1m"},
		{"limit-per-run 0", []string{"--limit-per-run", "0"}, nil, "--limit-per-run 0: must be > 0"},
		{"max-scan < limit-per-run", []string{"--limit-per-run", "50", "--max-scan", "49"}, nil, "--max-scan 49: must be >= --limit-per-run 50"},
		{"bad categories (query injection)", []string{"--categories", "cs.AI OR cat:x"}, nil, "--categories: invalid arXiv category"},
		{"empty categories", []string{"--categories", ""}, nil, "--categories: invalid arXiv category"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got *crawler.LoopConfig
			cmd := newCrawlLoopCmdWith(func(_ context.Context, lc crawler.LoopConfig) error {
				got = &lc
				return nil
			})
			cmd.SetArgs(c.args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			if c.want == nil {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) || got != nil {
					t.Fatalf("err %v, run called %v; want error %q and run not called", err, got != nil, c.wantErr)
				}
				return
			}
			if err != nil || got == nil || !reflect.DeepEqual(*got, *c.want) {
				t.Fatalf("err %v, run got %+v; want %+v", err, got, c.want)
			}
		})
	}
	cmd := newCrawlLoopCmd()
	for flag, want := range map[string]string{"categories": "cs.AI,cs.LG", "interval": "6h0m0s", "limit-per-run": "100", "max-scan": "2000", "once": "false"} {
		if f := cmd.Flags().Lookup(flag); f == nil || f.DefValue != want {
			t.Errorf("--%s default: %+v, want %q", flag, f, want)
		}
	}
}

// AC12 (ING-9b): a --rate-limit below arXiv's 3 s fails before any connection (cfg is nil here: a connect would panic).
func TestCrawlRateLimitFlag(t *testing.T) {
	for _, rl := range []string{"1s", "2.999s", "0s"} {
		cmd := newCrawlCmd()
		cmd.SetArgs([]string{"--rate-limit", rl})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--rate-limit "+rl+": must be at least 3s") {
			t.Errorf("--rate-limit %s: err %v, want the 3s minimum", rl, err)
		}
	}
	if f := newCrawlCmd().Flags().Lookup("rate-limit"); f.DefValue != "3s" {
		t.Errorf("--rate-limit default %s, want 3s", f.DefValue)
	}
	// Exactly 3s passes the check and fails only later, at the (unreachable) Postgres connection.
	saved := cfg
	t.Cleanup(func() { cfg = saved })
	cfg = &config.Config{PostgresURL: "postgres://u:p@127.0.0.1:1/x?sslmode=disable&connect_timeout=2", KafkaBrokers: []string{"127.0.0.1:1"}}
	cmd := newCrawlCmd()
	cmd.SetArgs([]string{"--rate-limit", "3s"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || strings.Contains(err.Error(), "must be at least") {
		t.Errorf("--rate-limit 3s: err %v, want it accepted (a connection error, not the minimum)", err)
	}
}
