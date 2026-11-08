package crawler

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestParseAtomFeed(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "arxiv_feed.xml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	papers, err := parseAtomFeed(body)
	if err != nil {
		t.Fatalf("parseAtomFeed: %v", err)
	}

	tests := []struct {
		arxivID    string
		version    int
		title      string
		abstract   string
		authors    []string
		categories []string
		published  time.Time
		sourceURL  string
	}{
		{
			arxivID:    "2307.08691",
			version:    2,
			title:      "FlashAttention-2: Faster Attention with Better Parallelism and Work Partitioning",
			abstract:   "Scaling Transformers to longer sequence lengths has been a major problem.",
			authors:    []string{"Tri Dao"},
			categories: []string{"cs.LG", "cs.AI"},
			published:  time.Date(2023, 7, 17, 17, 50, 36, 0, time.UTC),
			sourceURL:  "https://arxiv.org/pdf/2307.08691v2.pdf",
		},
		{
			arxivID:    "2401.12345",
			version:    1,
			title:      "A Paper Without Version",
			abstract:   "Short abstract.",
			authors:    []string{"Ada Lovelace", "Alan Turing"},
			categories: []string{"cs.CL"},
			published:  time.Date(2024, 1, 22, 9, 0, 0, 0, time.UTC),
			sourceURL:  "https://arxiv.org/pdf/2401.12345.pdf",
		},
	}

	if len(papers) != len(tests) {
		t.Fatalf("got %d papers, want %d", len(papers), len(tests))
	}

	for i, tt := range tests {
		t.Run(tt.arxivID, func(t *testing.T) {
			p := papers[i]
			if p.ArxivID != tt.arxivID {
				t.Errorf("ArxivID = %q, want %q", p.ArxivID, tt.arxivID)
			}
			if p.Version != tt.version {
				t.Errorf("Version = %d, want %d", p.Version, tt.version)
			}
			if p.Title != tt.title {
				t.Errorf("Title = %q, want %q", p.Title, tt.title)
			}
			if p.Abstract != tt.abstract {
				t.Errorf("Abstract = %q, want %q", p.Abstract, tt.abstract)
			}
			if !reflect.DeepEqual(p.Authors, tt.authors) {
				t.Errorf("Authors = %q, want %q", p.Authors, tt.authors)
			}
			if !reflect.DeepEqual(p.Categories, tt.categories) {
				t.Errorf("Categories = %q, want %q", p.Categories, tt.categories)
			}
			if !p.PublishedAt.Equal(tt.published) {
				t.Errorf("PublishedAt = %v, want %v", p.PublishedAt, tt.published)
			}
			if p.SourceURL != tt.sourceURL {
				t.Errorf("SourceURL = %q, want %q", p.SourceURL, tt.sourceURL)
			}
		})
	}
}

func TestParseAtomFeedEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantErr   bool
		wantCount int
	}{
		{name: "empty feed", body: `<feed xmlns="http://www.w3.org/2005/Atom"></feed>`, wantCount: 0},
		{name: "malformed xml", body: `<feed><entry>`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			papers, err := parseAtomFeed([]byte(tt.body))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if len(papers) != tt.wantCount {
				t.Errorf("got %d papers, want %d", len(papers), tt.wantCount)
			}
		})
	}
}
