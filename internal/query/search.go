package query

import (
	"context"
	"fmt"
	"strings"

	"arxivlens/internal/db"
)

type SearchHit struct {
	ChunkID      int64   `json:"chunk_id"`
	ArxivID      string  `json:"arxiv_id"`
	Title        string  `json:"title"`
	SectionTitle string  `json:"section"`
	Content      string  `json:"content"`
	RRFScore     float64 `json:"rrf_score"`
	RerankScore  float64 `json:"rerank_score,omitempty"`
}

type SearchEngine struct {
	database    *db.DB
	embedClient *EmbedClient
	modelName   string
}

func NewSearchEngine(database *db.DB, embedClient *EmbedClient, modelName string) *SearchEngine {
	if modelName == "" {
		modelName = "BAAI/bge-base-en-v1.5"
	}
	return &SearchEngine{
		database:    database,
		embedClient: embedClient,
		modelName:   modelName,
	}
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

	// 1. Generate query embedding vector
	vectors, err := s.embedClient.EmbedTexts(ctx, []string{queryString}, true)
	if err != nil {
		return nil, fmt.Errorf("failed to get query vector: %w", err)
	}
	if len(vectors) == 0 {
		return nil, fmt.Errorf("no vector returned for query")
	}

	vec := vectors[0]
	// Format as PostgreSQL halfvec literal string: "[0.123, -0.456, ...]"
	var sb strings.Builder
	sb.WriteString("[")
	for i, f := range vec {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(fmt.Sprintf("%.6f", f))
	}
	sb.WriteString("]")
	vecStr := sb.String()

	// 2. Hybrid RRF Search SQL Query matching §8
	querySQL := `
		WITH vector_hits AS (
			SELECT pc.id AS chunk_id,
			       ROW_NUMBER() OVER (ORDER BY ce.embedding <=> $1::halfvec) AS rank
			FROM chunk_embeddings ce
			JOIN paper_chunks pc ON pc.id = ce.chunk_id
			JOIN papers p        ON p.id  = pc.paper_id
			WHERE ce.model = $2
			  AND ($3::text[] IS NULL OR p.categories && $3)
			ORDER BY ce.embedding <=> $1::halfvec
			LIMIT 50
		),
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

	var catArray []string
	if len(categories) > 0 {
		catArray = categories
	}

	rows, err := s.database.Pool.Query(ctx, querySQL, vecStr, s.modelName, catArray, queryString, limit)
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

	// 3. Optional Second-Stage Reranker (§8)
	if rerank && len(hits) > 0 {
		passages := make([]string, len(hits))
		for i, h := range hits {
			passages[i] = h.Content
		}
		scores, err := s.embedClient.Rerank(ctx, queryString, passages)
		if err == nil && len(scores) == len(hits) {
			for i := range hits {
				hits[i].RerankScore = float64(scores[i])
			}
		}
	}

	return hits, nil
}
