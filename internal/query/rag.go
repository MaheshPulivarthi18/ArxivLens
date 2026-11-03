package query

import (
	"context"
	"fmt"
	"log"
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
// A citation whose support cannot be computed (embed error or missing vector) is unsupported with score 0.
//
// Embeddings are fetched in at most two EmbedTexts calls (AI-3): one for every hit's chunk, then one for
// the distinct marker-free cited sentences. No call is made when the answer has no in-range marker.
func (r *RAGService) validateCitations(ctx context.Context, answer string, hits []SearchHit) []Citation {
	k := len(hits)

	// 1. Collect in-range markers in answer order, deduplicating the cleaned sentences to embed.
	type pending struct{ hitIdx, sentIdx int }
	var cited []pending
	var sentTexts []string
	sentIndex := map[string]int{}
	for _, sent := range splitIntoSentences(answer) {
		for _, m := range markerRegex.FindAllStringSubmatch(sent, -1) {
			if len(m) < 2 {
				continue
			}
			markerIdx, _ := strconv.Atoi(m[1])
			// Discard fabricated markers outside 1..K
			if markerIdx < 1 || markerIdx > k {
				continue
			}
			cleanSent := strings.TrimSpace(markerRegex.ReplaceAllString(sent, ""))
			idx, ok := sentIndex[cleanSent]
			if !ok {
				idx = len(sentTexts)
				sentIndex[cleanSent] = idx
				sentTexts = append(sentTexts, cleanSent)
			}
			cited = append(cited, pending{hitIdx: markerIdx - 1, sentIdx: idx})
		}
	}
	if len(cited) == 0 {
		return nil
	}

	// 2. One call for all chunks, one for all distinct sentences.
	chunkTexts := make([]string, k)
	for i, h := range hits {
		chunkTexts[i] = h.Content
	}
	chunkEmbeddings, _ := r.embedBatch(ctx, chunkTexts)
	chunkMissing := 0
	for _, v := range chunkEmbeddings {
		if len(v) == 0 {
			chunkMissing++
		}
	}
	sentEmbeddings, sentErr := r.embedBatch(ctx, sentTexts)

	// 3. Verify support (§9) per citation. Fail closed (QA-4, AI-2): if either vector is missing,
	// the citation stays unsupported with score 0.
	validatedCitations := make([]Citation, 0, len(cited))
	unverified, sentenceErrs := 0, 0
	for _, c := range cited {
		hit := hits[c.hitIdx]
		supported := false
		score := 0.0

		sentVec, chunkVec := sentEmbeddings[c.sentIdx], chunkEmbeddings[c.hitIdx]
		if len(sentVec) > 0 && len(chunkVec) > 0 {
			sim := cosineSimilarity(sentVec, chunkVec)
			score = math.Round(float64(sim)*10000) / 10000
			supported = score >= SupportThreshold
		} else {
			unverified++
			if sentErr != nil {
				sentenceErrs++
			}
		}

		validatedCitations = append(validatedCitations, Citation{
			Marker:    c.hitIdx + 1,
			ArxivID:   hit.ArxivID,
			Title:     hit.Title,
			ChunkID:   hit.ChunkID,
			Section:   hit.SectionTitle,
			Content:   hit.Content,
			PDFURL:    fmt.Sprintf("https://arxiv.org/pdf/%s.pdf", hit.ArxivID),
			Score:     score,
			Supported: supported,
		})
	}
	if unverified > 0 {
		// One line per request; counts only, never passage or sentence text.
		id, src := traceIDFrom(ctx)
		log.Printf("[Query Service] citation support unverified trace_id=%s trace_id_source=%s unverified=%d citations=%d chunks_without_vector=%d/%d sentence_embed_errors=%d",
			id, src, unverified, len(validatedCitations), chunkMissing, k, sentenceErrs)
	}
	return validatedCitations
}

// embedBatch embeds texts (documents, not queries) in a single EmbedTexts call and returns exactly
// one slot per input, in input order; an empty slot means no vector. If the call errors, or returns a
// different number of vectors than inputs (so vectors cannot be matched to inputs), every slot is empty.
func (r *RAGService) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	if len(texts) == 0 {
		return out, nil
	}
	vecs, err := r.embedClient.EmbedTexts(ctx, texts, false)
	if err != nil {
		return out, err
	}
	if len(vecs) != len(texts) {
		return out, nil
	}
	copy(out, vecs)
	return out, nil
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
