package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// RET-2 (ADR-0014): search hits carry the paper version; page is omitted for every hit.

func intPtr(v int) *int { return &v }

func TestSearchHitJSON(t *testing.T) {
	cases := []struct {
		name      string
		hit       SearchHit
		want      []string // substrings that must appear
		forbidden []string // substrings that must not appear
	}{
		{"version always present, page nil omitted", SearchHit{ChunkID: 1, Version: 2},
			[]string{`"version":2`}, []string{`"page"`}},
		{"zero version still present", SearchHit{ChunkID: 1},
			[]string{`"version":0`}, []string{`"page"`}},
		{"page present when set", SearchHit{ChunkID: 1, Version: 1, Page: intPtr(4)},
			[]string{`"version":1`, `"page":4`}, nil},
		{"rerank score and version together", SearchHit{ChunkID: 1, Version: 3, RerankScore: 0.5},
			[]string{`"version":3`, `"rerank_score":0.5`}, []string{`"page"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.hit)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tc.want {
				if !bytes.Contains(b, []byte(w)) {
					t.Errorf("%s missing %s", b, w)
				}
			}
			for _, f := range tc.forbidden {
				if bytes.Contains(b, []byte(f)) {
					t.Errorf("%s contains %s", b, f)
				}
			}
		})
	}
}

// provVersion and provPage are the provenance a hit with this chunk ID was fetched with.
func provVersion(id int64) int { return int(id%3) + 1 }
func provPage(id int64) *int {
	if id%2 == 0 {
		return intPtr(int(id) * 10)
	}
	return nil
}

// Version and Page stay with their own hit through the no-rerank path, the rerank sort and every fallback.
func TestSearchHitProvenanceTravelsWithHit(t *testing.T) {
	cases := []struct {
		name   string
		rerank bool
		err    error
		scores func([]string) []float32
		want   []int64
	}{
		{"no rerank", false, nil, nil, []int64{1, 2, 3, 4, 5}},
		{"rerank reversed", true, nil, byRank(func(r int) float32 { return float32(r) }), []int64{20, 19, 18, 17, 16}},
		{"rerank rank 8 first", true, nil, byRank(func(r int) float32 {
			if r == 8 {
				return 1
			}
			return 0.5 - float32(r)*0.01
		}), []int64{8, 1, 2, 3, 4}},
		{"fallback rerank error", true, errors.New("boom"), nil, []int64{1, 2, 3, 4, 5}},
		{"fallback timeout", true, fmt.Errorf("w: %w", context.DeadlineExceeded), nil, []int64{1, 2, 3, 4, 5}},
		{"fallback count mismatch", true, nil, func(p []string) []float32 { return make([]float32, len(p)-1) }, []int64{1, 2, 3, 4, 5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &SearchEngine{rr: &fakeReranker{scores: tc.scores, err: tc.err},
				fetch: func(_ context.Context, _ string, _ []string, n int) ([]SearchHit, error) {
					hits := rrfHits(n)
					for i := range hits {
						hits[i].Version, hits[i].Page = provVersion(hits[i].ChunkID), provPage(hits[i].ChunkID)
					}
					return hits, nil
				}}
			hits, err := s.HybridSearch(context.Background(), "q", nil, 5, tc.rerank)
			if err != nil {
				t.Fatal(err)
			}
			if got := ids(hits); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("order = %v, want %v", got, tc.want)
			}
			for _, h := range hits {
				if h.Version != provVersion(h.ChunkID) || !reflect.DeepEqual(h.Page, provPage(h.ChunkID)) {
					t.Errorf("chunk %d: (version, page) = (%d, %v), want (%d, %v)",
						h.ChunkID, h.Version, h.Page, provVersion(h.ChunkID), provPage(h.ChunkID))
				}
			}
		})
	}
}

// outerSelectColumns returns the result column names of hybridSearchSQL's outer SELECT, in order.
func outerSelectColumns(t *testing.T, sql string) []string {
	t.Helper()
	i := strings.LastIndex(sql, "SELECT ")
	if i < 0 {
		t.Fatalf("outer SELECT not found in hybridSearchSQL")
	}
	j := strings.Index(sql[i:], "FROM paper_chunks c")
	if j < 0 {
		t.Fatalf("outer FROM not found in hybridSearchSQL")
	}
	list := sql[i+len("SELECT ") : i+j]
	var cols []string
	depth, start := 0, 0
	for k, r := range list + "," {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				expr := strings.TrimSpace((list + ",")[start:k])
				if a := strings.LastIndex(expr, " AS "); a >= 0 {
					expr = expr[a+len(" AS "):]
				}
				if expr != "" {
					cols = append(cols, expr)
				}
				start = k + 1
			}
		}
	}
	return cols
}

// The outer SELECT and the rows.Scan in fetchCandidates name the same fields in the same order, so a
// column/scan mismatch fails here rather than only on real pgvector (db.yml).
func TestSearchSQLColumnsMatchScan(t *testing.T) {
	field := map[string]string{"c.id": "ChunkID", "c.content": "Content", "p.arxiv_id": "ArxivID", "p.title": "Title",
		"section_title": "SectionTitle", "p.version": "Version", "rrf_score": "RRFScore"}
	var fromSQL []string
	for _, c := range outerSelectColumns(t, hybridSearchSQL) {
		f, ok := field[c]
		if !ok {
			t.Fatalf("outer SELECT column %q has no SearchHit field mapping", c)
		}
		fromSQL = append(fromSQL, f)
	}
	src, err := os.ReadFile("search.go")
	if err != nil {
		t.Fatal(err)
	}
	scans := regexp.MustCompile(`rows\.Scan\(([^)]*)\)`).FindAllSubmatch(src, -1)
	if len(scans) != 1 {
		t.Fatalf("found %d rows.Scan calls in search.go, want 1", len(scans))
	}
	var fromScan []string
	for _, m := range regexp.MustCompile(`&h\.(\w+)`).FindAllSubmatch(scans[0][1], -1) {
		fromScan = append(fromScan, string(m[1]))
	}
	want := []string{"ChunkID", "Content", "ArxivID", "Title", "SectionTitle", "Version", "RRFScore"}
	if !reflect.DeepEqual(fromSQL, want) || !reflect.DeepEqual(fromScan, want) {
		t.Fatalf("SELECT fields %v, scan fields %v, want both %v", fromSQL, fromScan, want)
	}
}

// AC4: RET-2 only adds ", p.version" to the outer SELECT. Everything else (both RRF arms, rrf_score,
// joins, ORDER BY, LIMIT) is byte-identical to the query before RET-2 (golden copy below).
func TestSearchSQLOuterSelectOnly(t *testing.T) {
	if strings.Count(hybridSearchSQL, " p.version,") != 1 {
		t.Fatalf("hybridSearchSQL must name p.version exactly once")
	}
	if vectorHitsSQL != goldenVectorHitsSQL {
		t.Fatalf("vectorHitsSQL changed:\n%s", vectorHitsSQL)
	}
	if got := strings.Replace(hybridSearchSQL, " p.version,", "", 1); got != goldenHybridSearchSQL {
		t.Fatalf("hybridSearchSQL changed beyond the outer SELECT:\n got %q\nwant %q", got, goldenHybridSearchSQL)
	}
}

// Golden copy of vectorHitsSQL and hybridSearchSQL at origin/main d3f9408 (before RET-2). Do not edit
// to make TestSearchSQLOuterSelectOnly pass: a ranking change needs its own ticket and eval run.
const goldenVectorHitsSQL = `
			SELECT pc.id AS chunk_id,
			       ROW_NUMBER() OVER (ORDER BY ce.embedding <=> $1::halfvec) AS rank
			FROM chunk_embeddings ce
			JOIN paper_chunks pc ON pc.id = ce.chunk_id
			JOIN papers p        ON p.id  = pc.paper_id
			WHERE ce.model = $2
			  AND ($3::text[] IS NULL OR p.categories && $3)
			ORDER BY ce.embedding <=> $1::halfvec
			LIMIT 50
`

// hybridSearchSQL is the hybrid RRF query (Technical PRD §8); $4 is the keyword query, $5 the limit.
const goldenHybridSearchSQL = `
		WITH vector_hits AS (` + goldenVectorHitsSQL + `		),
		keyword_hits AS (
			SELECT pc.id AS chunk_id,
			       ROW_NUMBER() OVER (ORDER BY ts_rank_cd(pc.content_tsv, q) DESC) AS rank
			FROM paper_chunks pc
			JOIN papers p ON p.id = pc.paper_id,
			     websearch_to_tsquery('english', $4) q
			WHERE pc.content_tsv @@ q
			  AND ($3::text[] IS NULL OR p.categories && $3)
			ORDER BY ts_rank_cd(pc.content_tsv, q) DESC
			LIMIT 50
		)
		SELECT c.id, c.content, p.arxiv_id, p.title, COALESCE(ps.title, '') AS section_title,
		       COALESCE(1.0/(60 + v.rank), 0) + COALESCE(1.0/(60 + k.rank), 0) AS rrf_score
		FROM paper_chunks c
		JOIN papers p ON p.id = c.paper_id
		LEFT JOIN paper_sections ps ON ps.id = c.section_id
		LEFT JOIN vector_hits  v ON v.chunk_id = c.id
		LEFT JOIN keyword_hits k ON k.chunk_id = c.id
		WHERE v.chunk_id IS NOT NULL OR k.chunk_id IS NOT NULL
		ORDER BY rrf_score DESC
		LIMIT $5;
	`
