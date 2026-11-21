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

// Paper diversity in the head of a reranked list (ADR-0016). While the first DiversityWindow results are chosen, a
// paper may hold at most PaperCap of them; the window is then filled from the remaining hits in rerank order, so a
// one-paper pool still returns DiversityWindow hits. Below the window the list stays in rerank order. Measured on
// the 78-case eval: Recall@5 0.95 -> 0.99 and multi-paper Recall@5 0.64 -> 0.86, MRR unchanged (0.96).
const (
	DiversityWindow = 5
	PaperCap        = 2
)

// FusionK is the rank-fusion constant (ADR-0017). Each candidate's order key is
// 1/(FusionK + rerank rank) + 1/(FusionK + first-stage RRF rank), so a passage the cross-encoder scores low but
// the first stage ranks at the top is not discarded. That happens on questions in the reader's own words, which
// share few terms with the passage. Measured on the 78-case eval with bge-reranker-base: MRR 0.964 -> 0.976,
// NDCG@5 0.961 -> 0.971, own-words MRR 0.69 -> 0.89, Recall@5 unchanged (0.985), no extra latency.
const FusionK = 10

type SearchHit struct {
	ChunkID      int64   `json:"chunk_id"`
	ArxivID      string  `json:"arxiv_id"`
	Title        string  `json:"title"`
	SectionTitle string  `json:"section"`
	Content      string  `json:"content"`
	RRFScore     float64 `json:"rrf_score"`
	RerankScore  float64 `json:"rerank_score,omitempty"`
	// Version is papers.version of the row that owns the chunk (ADR-0014, ING-7).
	Version int `json:"version"`
	// Page is the start page when recorded (paper_chunk_pages.page_start), omitted otherwise (ADR-0014, ING-11).
	// paper_chunks.page_number is the start page of the chunk's *section*, wrong for later chunks of a
	// multi-page section; do not "fix" a missing Page by selecting it.
	Page *int `json:"page,omitempty"`

	// poolRank is the hit's 1-based position in the ranked pool before the ADR-0016 top-5 cap: the ADR-0017 fused
	// order, or RRF order when rerank fell back. Ask uses it to re-rank the hits that pass its relevance cutoff.
	// Unexported, so never serialized.
	poolRank int
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
		SELECT c.id, c.content, p.arxiv_id, p.title, COALESCE(ps.title, '') AS section_title, p.version,
		       (SELECT cp.page_start FROM paper_chunk_pages cp WHERE cp.chunk_id = c.id) AS page,
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
		if err := rows.Scan(&h.ChunkID, &h.Content, &h.ArxivID, &h.Title, &h.SectionTitle, &h.Version, &h.Page, &h.RRFScore); err != nil {
			return nil, fmt.Errorf("failed to scan search hit: %w", err)
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hybrid search rows failed: %w", err)
	}

	return hits, nil
}

// rerankAndSort scores hits with one Rerank call, orders them by fused rerank and RRF rank (ADR-0017; ties keep
// RRF order), applies the top-5 paper cap (ADR-0016) and truncates to limit. On a rerank error, a score-count mismatch
// or a NaN or ±Inf score it keeps RRF order, leaves RerankScore at 0 and logs the trace ID. ±Inf falls back like NaN:
// encoding/json can't encode it, so /v1/search would send a truncated body.
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
			if math.IsInf(float64(sc), 0) {
				reason = "Inf score"
				break
			}
		}
	}
	if reason != "" {
		id, src := traceIDFrom(ctx)
		log.Printf("[Query Service] rerank fallback trace_id=%s trace_id_source=%s reason=%q pool=%d scores=%d",
			id, src, reason, len(hits), len(scores))
		setPoolRanks(hits)
		return hits[:min(limit, len(hits))]
	}

	for i := range hits {
		hits[i].RerankScore = float64(scores[i])
	}
	hits = fuseRanks(hits)
	setPoolRanks(hits)
	hits = diversify(hits, DiversityWindow, PaperCap)
	return hits[:min(limit, len(hits))]
}

func setPoolRanks(hits []SearchHit) {
	for i := range hits {
		hits[i].poolRank = i + 1
	}
}

// fuseRanks orders hits (given in first-stage RRF order, RerankScore set) by
// 1/(FusionK + rerank rank) + 1/(FusionK + RRF rank), both ranks 1-based. Rerank ranks come from a stable sort by
// RerankScore, so equal scores keep RRF order; equal fused keys also keep RRF order. RerankScore is unchanged: the
// /v1/ask relevance cutoff still reads the cross-encoder's own probability.
func fuseRanks(hits []SearchHit) []SearchHit {
	n := len(hits)
	byScore := make([]int, n)
	for i := range byScore {
		byScore[i] = i
	}
	sort.SliceStable(byScore, func(a, b int) bool { return hits[byScore[a]].RerankScore > hits[byScore[b]].RerankScore })
	key := make([]float64, n)
	for rank, i := range byScore {
		key[i] = 1/float64(FusionK+rank+1) + 1/float64(FusionK+i+1)
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return key[order[a]] > key[order[b]] })
	out := make([]SearchHit, n)
	for pos, i := range order {
		out[pos] = hits[i]
	}
	return out
}

// diversify reorders a ranked list so that its first window entries hold at most perPaper hits from any one paper
// (ArxivID), taking hits greedily in rank order. Skipped hits keep their relative order and fill any slots the cap
// leaves empty, then follow the window. The result is a permutation of hits.
func diversify(hits []SearchHit, window, perPaper int) []SearchHit {
	if len(hits) <= 1 || window <= 0 || perPaper <= 0 {
		return hits
	}
	head := make([]SearchHit, 0, len(hits))
	var rest []SearchHit
	count := map[string]int{}
	for _, h := range hits {
		if len(head) < window && count[h.ArxivID] < perPaper {
			head = append(head, h)
			count[h.ArxivID]++
			continue
		}
		rest = append(rest, h)
	}
	fill := min(window-len(head), len(rest))
	if fill < 0 {
		fill = 0
	}
	head = append(head, rest[:fill]...)
	return append(head, rest[fill:]...)
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
