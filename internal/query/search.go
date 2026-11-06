package query

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"

	"arxivlens/internal/db"

	"github.com/jackc/pgx/v5"
)

// RerankPool is the number of RRF candidates reranked when rerank=true (ADR-0010).
// The pool is max(RerankPool, limit). If latency forces a smaller value, record it in ADR-0010.
const RerankPool = 20

type SearchHit struct {
	ChunkID      int64   `json:"chunk_id"`
	ArxivID      string  `json:"arxiv_id"`
	Title        string  `json:"title"`
	SectionTitle string  `json:"section"`
	Content      string  `json:"content"`
	RRFScore     float64 `json:"rrf_score"`
	RerankScore  float64 `json:"rerank_score,omitempty"`
}

// reranker is the one method HybridSearch needs from *EmbedClient.
type reranker interface {
	Rerank(ctx context.Context, query string, passages []string) ([]float32, error)
}

type SearchEngine struct {
	database    *db.DB
	embedClient *EmbedClient
	modelName   string

	// Test seams; NewSearchEngine wires the real implementations.
	rr    reranker
	fetch func(ctx context.Context, query string, categories []string, n int) ([]SearchHit, error)
}

func NewSearchEngine(database *db.DB, embedClient *EmbedClient, modelName string) *SearchEngine {
	if modelName == "" {
		modelName = "BAAI/bge-base-en-v1.5"
	}
	s := &SearchEngine{
		database:    database,
		embedClient: embedClient,
		modelName:   modelName,
	}
	s.rr = embedClient
	s.fetch = s.fetchCandidates
	return s
}

func (s *SearchEngine) HybridSearch(
	ctx context.Context,
	queryString string,
	categories []string,
	limit int,
	rerank bool,
) ([]SearchHit, error) {
	if limit <= 0 {
		limit = 20
	}

	if !rerank {
		return s.fetch(ctx, queryString, categories, limit)
	}

	// Rerank a candidate pool of max(RerankPool, limit), then sort and truncate (ADR-0010).
	hits, err := s.fetch(ctx, queryString, categories, max(RerankPool, limit))
	if err != nil {
		return nil, err
	}
	return rerankAndSort(ctx, s.rr, queryString, hits, limit), nil
}

// vectorHitsSQL is the vector arm of hybridSearchSQL ($1 query halfvec, $2 model, $3 categories or NULL).
// It is a separate constant so the integration test (search_integration_test.go) measures this exact text.
const vectorHitsSQL = `
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
const hybridSearchSQL = `
		WITH vector_hits AS (` + vectorHitsSQL + `		),
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

// hnswSearchSettings run with SET LOCAL before hybridSearchSQL (OPS-H5). With pgvector's default
// hnsw.ef_search=40 the HNSW scan yields at most 40 tuples before the model/category filters, so
// vector_hits could not fill its 50-row limit. ef_search=100 plus strict_order iterative scan
// (pgvector >= 0.8.0) keeps scanning until the limit is filled after filtering, in distance order.
const hnswSearchSettings = `SET LOCAL hnsw.ef_search = 100; SET LOCAL hnsw.iterative_scan = strict_order`

// halfvecLiteral formats vec as a PostgreSQL halfvec literal: "[0.123000,-0.456000,...]".
func halfvecLiteral(vec []float32) string {
	var sb strings.Builder
	sb.WriteString("[")
	for i, f := range vec {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(fmt.Sprintf("%.6f", f))
	}
	sb.WriteString("]")
	return sb.String()
}

// fetchCandidates embeds the query and returns the top n hybrid RRF hits in rrf_score order.
func (s *SearchEngine) fetchCandidates(ctx context.Context, queryString string, categories []string, n int) ([]SearchHit, error) {
	// 1. Generate query embedding vector
	vectors, err := s.embedClient.EmbedTexts(ctx, []string{queryString}, true)
	if err != nil {
		return nil, fmt.Errorf("failed to get query vector: %w", err)
	}
	if len(vectors) == 0 {
		return nil, fmt.Errorf("no vector returned for query")
	}

	vecStr := halfvecLiteral(vectors[0])

	var catArray []string
	if len(categories) > 0 {
		catArray = categories
	}

	// 2. Hybrid RRF search in a read-only transaction so the HNSW settings stay local to it.
	tx, err := s.database.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("hybrid search begin failed: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, hnswSearchSettings); err != nil {
		return nil, fmt.Errorf("hybrid search settings failed: %w", err)
	}

	rows, err := tx.Query(ctx, hybridSearchSQL, vecStr, s.modelName, catArray, queryString, n)
	if err != nil {
		return nil, fmt.Errorf("hybrid search query failed: %w", err)
	}
	defer rows.Close()

	var hits []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.ChunkID, &h.Content, &h.ArxivID, &h.Title, &h.SectionTitle, &h.RRFScore); err != nil {
			return nil, fmt.Errorf("failed to scan search hit: %w", err)
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hybrid search rows failed: %w", err)
	}

	return hits, nil
}

// rerankAndSort scores hits with one Rerank call, stable-sorts by RerankScore descending
// (ties keep RRF order) and truncates to limit. On a rerank error, a score-count mismatch
// or a NaN score it keeps RRF order, leaves RerankScore at 0 and logs the trace ID.
func rerankAndSort(ctx context.Context, rr reranker, query string, hits []SearchHit, limit int) []SearchHit {
	if len(hits) == 0 {
		return hits
	}
	passages := make([]string, len(hits))
	for i, h := range hits {
		passages[i] = h.Content
	}
	scores, err := rr.Rerank(ctx, query, passages)

	reason := ""
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		reason = "rerank timeout"
	case errors.Is(err, context.Canceled):
		reason = "rerank canceled"
	case err != nil:
		// Not err.Error(): it can carry the embedder's response body, which may echo query/passages.
		reason = "rerank error"
	case len(scores) != len(hits):
		reason = "score count mismatch"
	default:
		for _, sc := range scores {
			if math.IsNaN(float64(sc)) {
				reason = "NaN score"
				break
			}
		}
	}
	if reason != "" {
		id, src := traceIDFrom(ctx)
		log.Printf("[Query Service] rerank fallback trace_id=%s trace_id_source=%s reason=%q pool=%d scores=%d",
			id, src, reason, len(hits), len(scores))
		return hits[:min(limit, len(hits))]
	}

	for i := range hits {
		hits[i].RerankScore = float64(scores[i])
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].RerankScore > hits[j].RerankScore })
	return hits[:min(limit, len(hits))]
}

type traceIDKey struct{}

// withTraceID stores a trace ID in ctx for downstream log lines.
func withTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey{}, id)
}

// traceIDFrom returns the trace ID stored by withTraceID ("ctx"), else a generated one ("generated").
func traceIDFrom(ctx context.Context) (id, source string) {
	if v, ok := ctx.Value(traceIDKey{}).(string); ok && v != "" {
		return v, "ctx"
	}
	return generateTraceID(), "generated"
}
