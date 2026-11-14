//go:build integration

// Provenance integration test for /v1/search hits (RET-2, ADR-0014). It needs a migrated PostgreSQL +
// pgvector database and runs the production path (HybridSearch → fetchCandidates → hybridSearchSQL + scan):
//
//	DATABASE_URL=postgres://... go test -tags integration -v -run TestSearchProvenance ./internal/query/
package query

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"arxivlens/internal/db"
)

func TestSearchProvenance(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	d, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close) // registered first so it runs last, after the seed cleanup (t.Cleanup is LIFO)

	// Every chunk and the query share one direction, so the vector arm returns all three; the keyword
	// arm matches "zyxprov". Category test.prov keeps other rows (TestHNSW's corpus) out of both arms.
	vec := make([]float32, 768)
	for i := range vec {
		vec[i] = 0.036
	}
	lit := halfvecLiteral(vec)
	cleanup := func() {
		if _, err := d.Pool.Exec(ctx, `DELETE FROM papers WHERE arxiv_id LIKE 'prov.%'`); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	type seedChunk struct {
		version int
		index   int
		content string
		page    *int // stored paper_chunks.page_number (section start page); never returned
	}
	seed := []seedChunk{
		{1, 0, "zyxprov alpha one", nil},
		{2, 0, "zyxprov alpha two", intPtr(4)},
		{2, 1, "zyxprov beta two", intPtr(7)},
	}
	paperID := map[int]int64{}
	for _, v := range []int{1, 2} {
		var id int64
		if err := d.Pool.QueryRow(ctx, `INSERT INTO papers (arxiv_id, version, title, categories)
			VALUES ('prov.00001', $1, $2, ARRAY['test.prov']) RETURNING id`, v, "Provenance v"+strconv.Itoa(v)).Scan(&id); err != nil {
			t.Fatalf("seed paper v%d: %v", v, err)
		}
		paperID[v] = id
	}
	wantVersion := map[int64]int{}
	for _, c := range seed {
		var id int64
		if err := d.Pool.QueryRow(ctx, `INSERT INTO paper_chunks (paper_id, chunk_index, content, content_hash, page_number)
			VALUES ($1, $2, $3, md5($3), $4) RETURNING id`, paperID[c.version], c.index, c.content, c.page).Scan(&id); err != nil {
			t.Fatalf("seed chunk %q: %v", c.content, err)
		}
		if _, err := d.Pool.Exec(ctx, `INSERT INTO chunk_embeddings (chunk_id, model, embedding) VALUES ($1, $2, $3::halfvec)`,
			id, itModel, lit); err != nil {
			t.Fatalf("seed embedding: %v", err)
		}
		wantVersion[id] = c.version
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/embed" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(EmbedResponse{Vectors: [][]float32{vec}, Dim: len(vec)})
	}))
	defer srv.Close()

	engine := NewSearchEngine(d, NewEmbedClient(srv.URL), itModel)
	hits, err := engine.HybridSearch(ctx, "zyxprov", []string{"test.prov"}, 10, false)
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	for _, h := range hits {
		b, _ := json.Marshal(h)
		t.Logf("PROVENANCE chunk_id=%d arxiv_id=%s version=%d page=%v rrf_score=%.6f json=%s",
			h.ChunkID, h.ArxivID, h.Version, h.Page, h.RRFScore, b)
	}

	// 1. all three seeded chunks, nothing else
	if len(hits) != len(seed) {
		t.Fatalf("got %d hits, want %d", len(hits), len(seed))
	}
	for _, h := range hits {
		want, ok := wantVersion[h.ChunkID]
		if !ok || h.ArxivID != "prov.00001" {
			t.Fatalf("unexpected hit chunk %d (%s)", h.ChunkID, h.ArxivID)
		}
		// 2. version of the papers row that owns the chunk; 3. the title comes from the same row
		if h.Version != want {
			t.Errorf("chunk %d: version = %d, want %d", h.ChunkID, h.Version, want)
		}
		if wantTitle := "Provenance v" + strconv.Itoa(want); h.Title != wantTitle {
			t.Errorf("chunk %d: title = %q, want %q", h.ChunkID, h.Title, wantTitle)
		}
		// 4. no hit carries page, whether page_number is NULL or stored (4, 7): it is the section start page
		b, err := json.Marshal(h)
		if err != nil {
			t.Fatal(err)
		}
		if h.Page != nil || strings.Contains(string(b), `"page"`) {
			t.Errorf("chunk %d: page leaked (Page=%v, json=%s)", h.ChunkID, h.Page, b)
		}
		if !strings.Contains(string(b), `"version":`+strconv.Itoa(want)) {
			t.Errorf("chunk %d: json %s has no version %d", h.ChunkID, b, want)
		}
	}
}
