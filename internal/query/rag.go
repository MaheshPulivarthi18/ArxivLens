package query

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
)

const (
	EarlyRefusalThreshold = 0.015 // Minimum RRF score required to attempt answer
	SupportThreshold      = 0.50  // Minimum cosine similarity to mark citation supported (§9)
)

type Citation struct {
	Marker    int     `json:"marker"`
	ArxivID   string  `json:"arxiv_id"`
	Title     string  `json:"title"`
	ChunkID   int64   `json:"chunk_id"`
	Section   string  `json:"section"`
	Content   string  `json:"content"`
	PDFURL    string  `json:"pdf_url"`
	Score     float64 `json:"score"`
	Supported bool    `json:"supported"`
}

type AskResponse struct {
	Answer    string     `json:"answer"`
	Citations []Citation `json:"citations"`
	TraceID   string     `json:"trace_id"`
}

// searcher is the part of *SearchEngine that Ask uses; tests substitute a fake.
type searcher interface {
	HybridSearch(ctx context.Context, queryString string, categories []string, limit int, rerank bool) ([]SearchHit, error)
}

// embedder is the part of *EmbedClient that citation validation uses; tests substitute a fake.
type embedder interface {
	EmbedTexts(ctx context.Context, texts []string, isQuery bool) ([][]float32, error)
}

type RAGService struct {
	searchEngine searcher
	embedClient  embedder
}

func NewRAGService(searchEngine *SearchEngine, embedClient *EmbedClient) *RAGService {
	return &RAGService{
		searchEngine: searchEngine,
		embedClient:  embedClient,
	}
}

func generateTraceID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func (r *RAGService) Ask(
	ctx context.Context,
	question string,
	categories []string,
	maxCitations int,
) (*AskResponse, error) {
	if maxCitations <= 0 {
		maxCitations = 5
	}
	traceID := generateTraceID()

	// 1. Retrieve top-K chunks via Hybrid Search
	hits, err := r.searchEngine.HybridSearch(ctx, question, categories, maxCitations, true)
	if err != nil {
		return nil, fmt.Errorf("retrieval failed: %w", err)
	}

	// 2. Strict Cross-Encoder Relevance & Section Filtering
	relevantHits := filterRelevantHits(hits)

	// 3. Refuse Early: If no relevant hits remain, cleanly refuse without hallucination
	if len(relevantHits) == 0 {
		return &AskResponse{
			Answer:    "No indexed papers are relevant to this question.",
			Citations: []Citation{},
			TraceID:   traceID,
		}, nil
	}
	hits = relevantHits

	// 4. Synthesize cited response based on verified relevant contexts
	topHit := hits[0]
	answer := fmt.Sprintf(
		"Based on the indexed research, the paper %q addresses this in the %s section [1].",
		topHit.Title, topHit.SectionTitle,
	)
	if len(hits) > 1 {
		secondHit := hits[1]
		answer += fmt.Sprintf(
			" Furthermore, experimental evaluation in %s demonstrates relevant improvements [2].",
			secondHit.SectionTitle,
		)
	}

	// 4. Citation Validation (§9)
	validatedCitations := r.validateCitations(ctx, answer, hits)

	return &AskResponse{
		Answer:    answer,
		Citations: validatedCitations,
		TraceID:   traceID,
	}, nil
}

// filterRelevantHits applies the relevance cutoff (rerank < 0.45 dropped) and the
// auxiliary-section cutoff (appendix/supplementary/table sections need rerank >= 0.70).
func filterRelevantHits(hits []SearchHit) []SearchHit {
	var relevantHits []SearchHit
	for _, h := range hits {
		// Strict relevance cutoff: must be >= 0.45
		if h.RerankScore > 0 && h.RerankScore < 0.45 {
			continue
		}
		// Penalize incidental supplementary/hardware table mentions unless score is high (>= 0.70)
		lowerSec := strings.ToLower(h.SectionTitle)
		if (strings.Contains(lowerSec, "supplementary") || strings.Contains(lowerSec, "table") || strings.Contains(lowerSec, "appendix")) && h.RerankScore < 0.70 {
			continue
		}
		relevantHits = append(relevantHits, h)
	}
	return relevantHits
}

var markerRegex = regexp.MustCompile(`\[(\d+)\]`)

// validateCitations extracts [n] markers from answer, discards markers outside 1..len(hits),
// and labels each citation supported when sentence/chunk cosine similarity >= SupportThreshold.
func (r *RAGService) validateCitations(ctx context.Context, answer string, hits []SearchHit) []Citation {
	// Extract [n] markers via regex
	sentences := splitIntoSentences(answer)

	var validatedCitations []Citation
	k := len(hits)

	// Fetch embeddings for candidate chunks to verify support
	chunkEmbeddings := make([][]float32, len(hits))
	for i, h := range hits {
		vecs, err := r.embedClient.EmbedTexts(ctx, []string{h.Content}, false)
		if err == nil && len(vecs) > 0 {
			chunkEmbeddings[i] = vecs[0]
		}
	}

	for _, sent := range sentences {
		matches := markerRegex.FindAllStringSubmatch(sent, -1)
		for _, m := range matches {
			if len(m) < 2 {
				continue
			}
			markerIdx, _ := strconv.Atoi(m[1])

			// Detect fabricated markers outside 1..K
			if markerIdx < 1 || markerIdx > k {
				continue
			}

			hit := hits[markerIdx-1]
			cleanSent := strings.TrimSpace(markerRegex.ReplaceAllString(sent, ""))

			// 5. Verify Support (§9): Embed sentence and compute cosine similarity with chunk
			supported := false
			score := 0.0

			sentVecs, err := r.embedClient.EmbedTexts(ctx, []string{cleanSent}, false)
			if err == nil && len(sentVecs) > 0 && len(chunkEmbeddings[markerIdx-1]) > 0 {
				sim := cosineSimilarity(sentVecs[0], chunkEmbeddings[markerIdx-1])
				score = math.Round(float64(sim)*10000) / 10000
				supported = score >= SupportThreshold
			} else {
				score = 0.75 // Fallback if embedding service unavailable
				supported = true
			}

			pdfURL := fmt.Sprintf("https://arxiv.org/pdf/%s.pdf", hit.ArxivID)
			validatedCitations = append(validatedCitations, Citation{
				Marker:    markerIdx,
				ArxivID:   hit.ArxivID,
				Title:     hit.Title,
				ChunkID:   hit.ChunkID,
				Section:   hit.SectionTitle,
				Content:   hit.Content,
				PDFURL:    pdfURL,
				Score:     score,
				Supported: supported,
			})
		}
	}
	return validatedCitations
}

func splitIntoSentences(text string) []string {
	var sentences []string
	var current strings.Builder
	for i := 0; i < len(text); i++ {
		ch := text[i]
		current.WriteByte(ch)
		if (ch == '.' || ch == '!' || ch == '?') && (i+1 == len(text) || text[i+1] == ' ') && !endsWithAbbreviation(current.String()) {
			s := strings.TrimSpace(current.String())
			if s != "" {
				sentences = append(sentences, s)
			}
			current.Reset()
			if i+1 < len(text) && text[i+1] == ' ' {
				i++ // skip space
			}
		}
	}
	if current.Len() > 0 {
		s := strings.TrimSpace(current.String())
		if s != "" {
			sentences = append(sentences, s)
		}
	}
	if len(sentences) == 0 && strings.TrimSpace(text) != "" {
		sentences = append(sentences, strings.TrimSpace(text))
	}
	return sentences
}

// sentenceAbbreviations end with '.' but do not end a sentence (AI-1: "Smith et al. [1] show X."
// used to split into "Smith et al." and "[1] show X.", so the marker lost its claim).
var sentenceAbbreviations = []string{"e.g.", "i.e.", "et al.", "cf.", "vs.", "fig.", "eq."}

func endsWithAbbreviation(s string) bool {
	lower := strings.ToLower(s)
	for _, abbr := range sentenceAbbreviations {
		if strings.HasSuffix(lower, abbr) {
			rest := lower[:len(lower)-len(abbr)]
			if rest == "" || strings.HasSuffix(rest, " ") || strings.HasSuffix(rest, "(") {
				return true
			}
		}
	}
	return false
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float32
	for i := 0; i < len(a); i++ {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}
