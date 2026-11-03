//go:build integration

// HNSW integration test for the vector arm of the hybrid query (OPS-H5). It needs a migrated
// PostgreSQL + pgvector database and seeds a deterministic synthetic corpus into it:
//
//	DATABASE_URL=postgres://... go test -tags integration -v -run TestHNSW ./internal/query/
//
// It plans and runs the production constants (vectorHitsSQL, hybridSearchSQL, hnswSearchSettings),
// not copies, and logs EXPLAIN (ANALYZE, BUFFERS) plus result count and Recall@5 against an exact scan.
package query

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"arxivlens/internal/db"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const itModel = "BAAI/bge-base-en-v1.5"

// 1000 papers x 10 chunks around 100 random centres; every chunk has an itModel vector, 30% also
// have a vector for a second model (filtered out by ce.model). cs.DL tags 2% of papers.
const itSeedSQL = `
SET max_parallel_workers_per_gather = 0;
SELECT setseed(0.42);
CREATE TEMP TABLE it_centers AS
  SELECT c, array_agg((random() - 0.5)::real ORDER BY d) AS v
  FROM generate_series(1, 100) c, generate_series(1, 768) d GROUP BY c;
INSERT INTO papers (arxiv_id, title, categories)
  SELECT 'synth.' || lpad(j::text, 5, '0'), 'Synthetic paper ' || j,
         (ARRAY['cs.CL','cs.LG','cs.IR','stat.ML'])[1 + j % 4:1 + j % 4] ||
         CASE WHEN j % 50 = 0 THEN ARRAY['cs.DL'] ELSE ARRAY[]::text[] END
  FROM generate_series(1, 1000) j;
INSERT INTO paper_chunks (paper_id, chunk_index, content, content_hash)
  SELECT p.id, k, 'synthetic chunk ' || p.id || '-' || k, md5(p.id || '-' || k)
  FROM papers p, generate_series(0, 9) k ORDER BY p.id, k;
CREATE TEMP TABLE it_scale AS
  SELECT id, 0.05 + 0.5 * random() AS s FROM paper_chunks ORDER BY id;
INSERT INTO chunk_embeddings (chunk_id, model, embedding)
  SELECT x.id, m.model, x.v::halfvec(768) FROM (
    SELECT sc.id, array_agg((c.v[d] + sc.s * (random() - 0.5))::real ORDER BY d) AS v
    FROM it_scale sc JOIN it_centers c ON c.c = 1 + sc.id % 100, generate_series(1, 768) d
    GROUP BY sc.id ORDER BY sc.id) x
  CROSS JOIN LATERAL (SELECT '` + itModel + `' AS model
                      UNION ALL SELECT 'other/model-v0' WHERE x.id % 10 < 3) m;
ANALYZE;
`

const itQueryVecSQL = `
SELECT (array_agg((c.v[d] + 0.05 * (random() - 0.5))::real ORDER BY d))::halfvec(768)::text
FROM it_centers c, generate_series(1, 768) d WHERE c.c = $1 GROUP BY c.c`

func TestHNSWVectorHits(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	d, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	conn, err := d.Pool.Acquire(ctx) // one session: the seed uses temp tables
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()

	var extVersion string
	if err := conn.QueryRow(ctx, `SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&extVersion); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	if _, err := conn.Exec(ctx, itSeedSQL); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var nEmb int
	_ = conn.QueryRow(ctx, `SELECT count(*) FROM chunk_embeddings`).Scan(&nEmb)
	t.Logf("pgvector %s; seeded %d embeddings in %s", extVersion, nEmb, time.Since(t0).Round(time.Millisecond))

	var queries []string
	for q := 0; q < 20; q++ {
		var v string
		if err := conn.QueryRow(ctx, itQueryVecSQL, 1+q*5).Scan(&v); err != nil {
			t.Fatal(err)
		}
		queries = append(queries, v)
	}

	const exact = "SET LOCAL enable_indexscan = off"
	configs := []struct{ name, settings string }{
		{"default (ef_search=40)", ""},
		{"ef_search=100", "SET LOCAL hnsw.ef_search = 100"},
		{"ef_search=40 + iterative strict_order", "SET LOCAL hnsw.iterative_scan = strict_order"},
		{"query path (hnswSearchSettings)", hnswSearchSettings},
	}
	filters := []struct {
		name string
		cats []string
	}{{"no category filter", nil}, {"cs.CL (~25% of papers)", []string{"cs.CL"}}, {"cs.DL (~2% of papers)", []string{"cs.DL"}}}

	for _, f := range filters {
		base := make([][]int64, len(queries))
		for i, q := range queries {
			base[i], _ = itRun(t, ctx, conn, exact, q, f.cats)
		}
		for _, c := range configs {
			var sumCount, minCount, short int
			var sumR5, sumR50 float64
			var dur time.Duration
			minCount = 1 << 30
			for i, q := range queries {
				got, took := itRun(t, ctx, conn, c.settings, q, f.cats)
				dur += took
				sumCount += len(got)
				minCount = min(minCount, len(got))
				sumR5 += itRecall(got, base[i], 5)
				sumR50 += itRecall(got, base[i], 50)
				if len(got) < len(base[i]) {
					short++
					if c.settings == hnswSearchSettings {
						t.Errorf("%s / %s: query %d returned %d rows, exact scan %d", f.name, c.name, i, len(got), len(base[i]))
					}
				}
			}
			n := float64(len(queries))
			t.Logf("RESULT filter=%-24q config=%-40q mean_rows=%.1f min_rows=%d short_queries=%d/%d recall@5=%.3f recall@50=%.3f mean_ms=%.2f (exact mean_rows=%.1f)",
				f.name, c.name, float64(sumCount)/n, minCount, short, len(queries), sumR5/n, sumR50/n,
				float64(dur.Microseconds())/1000/n, itMeanLen(base))
		}
	}

	explain := func(label, settings, sql string, args ...any) {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if settings != "" {
			if _, err := tx.Exec(ctx, settings); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
		if err != nil {
			t.Fatalf("explain %s: %v", label, err)
		}
		var plan []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, line)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		text := strings.Join(plan, "\n")
		uses := strings.Contains(text, "chunk_embeddings_hnsw_idx")
		t.Logf("EXPLAIN %s\nPLAN-HNSW-SELECTED=%v\n%s", label, uses, text)
		if settings == exact && uses {
			t.Errorf("%s: exact baseline used the HNSW index", label)
		}
	}
	for _, f := range filters {
		for _, s := range []struct{ name, settings string }{{"exact", exact}, {"default", ""}, {"query path", hnswSearchSettings}} {
			explain(fmt.Sprintf("vectorHitsSQL [%s, %s]", s.name, f.name), s.settings, vectorHitsSQL, queries[0], itModel, f.cats)
		}
		explain(fmt.Sprintf("hybridSearchSQL [query path, %s]", f.name), hnswSearchSettings, hybridSearchSQL,
			queries[0], itModel, f.cats, "synthetic chunk", 20)
	}
}

// itRun runs vectorHitsSQL in a transaction after settings and returns the chunk IDs in rank order.
func itRun(t *testing.T, ctx context.Context, conn *pgxpool.Conn, settings, vec string, cats []string) ([]int64, time.Duration) {
	t.Helper()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if settings != "" {
		if _, err := tx.Exec(ctx, settings); err != nil {
			t.Fatalf("settings %q: %v", settings, err)
		}
	}
	start := time.Now()
	rows, err := tx.Query(ctx, vectorHitsSQL, vec, itModel, cats)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id, rank int64
		if err := rows.Scan(&id, &rank); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids, time.Since(start)
}

// itRecall is |top k of got ∩ top k of want| / min(k, len(want)).
func itRecall(got, want []int64, k int) float64 {
	want = want[:min(k, len(want))]
	if len(want) == 0 {
		return 1
	}
	in := map[int64]bool{}
	for _, id := range got[:min(k, len(got))] {
		in[id] = true
	}
	hit := 0
	for _, id := range want {
		if in[id] {
			hit++
		}
	}
	return float64(hit) / float64(len(want))
}

func itMeanLen(xs [][]int64) float64 {
	s := 0
	for _, x := range xs {
		s += len(x)
	}
	return float64(s) / float64(len(xs))
}
