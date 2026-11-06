package query

import (
	"testing"
	"time"

	"arxivlens/internal/config"
)

// Pins current behaviour of ExtractCleanArxivID (regex \d{4}\.\d{4,5}).
// Old-style IDs such as cs/0601001 are not supported today.
func TestExtractCleanArxivID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "bare 5-digit suffix", input: "2307.08691", want: "2307.08691"},
		{name: "bare 4-digit suffix", input: "1706.0376", want: "1706.0376"},
		{name: "versioned v2", input: "2307.08691v2", want: "2307.08691"},
		{name: "versioned v12", input: "2401.12345v12", want: "2401.12345"},
		{name: "abs URL", input: "https://arxiv.org/abs/2307.08691", want: "2307.08691"},
		{name: "abs URL versioned", input: "http://arxiv.org/abs/2307.08691v3", want: "2307.08691"},
		{name: "pdf URL", input: "https://arxiv.org/pdf/2307.08691", want: "2307.08691"},
		{name: "pdf URL with .pdf suffix", input: "https://arxiv.org/pdf/2307.08691v2.pdf", want: "2307.08691"},
		{name: ".pdf suffix", input: "2307.08691.pdf", want: "2307.08691"},
		{name: "surrounding whitespace", input: "  \t2307.08691\n ", want: "2307.08691"},
		{name: "empty string", input: "", wantErr: true},
		{name: "whitespace only", input: "   ", wantErr: true},
		{name: "free text", input: "attention is all you need", wantErr: true},
		{name: "old-style ID unsupported", input: "cs/0601001", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractCleanArxivID(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ExtractCleanArxivID(%q) = %q, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExtractCleanArxivID(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ExtractCleanArxivID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNewPaperIngesterTimeouts(t *testing.T) {
	tests := []struct {
		name          string
		ingestTimeout time.Duration
		wantIngest    time.Duration
	}{
		{name: "explicit", ingestTimeout: 7 * time.Minute, wantIngest: 7 * time.Minute},
		{name: "zero uses default", ingestTimeout: 0, wantIngest: config.DefaultIngestTimeout},
		{name: "negative uses default", ingestTimeout: -time.Second, wantIngest: config.DefaultIngestTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ing := NewPaperIngester(nil, nil, "bucket", "", tt.ingestTimeout)
			if ing.ingestClient == nil || ing.httpClient == nil {
				t.Fatalf("clients not initialised")
			}
			if got := ing.ingestClient.Timeout; got != tt.wantIngest {
				t.Fatalf("ingest timeout = %s, want %s", got, tt.wantIngest)
			}
			// arXiv metadata/PDF fetches keep their own shorter timeout.
			if got := ing.httpClient.Timeout; got != 45*time.Second {
				t.Fatalf("arXiv client timeout = %s, want 45s", got)
			}
		})
	}
}
