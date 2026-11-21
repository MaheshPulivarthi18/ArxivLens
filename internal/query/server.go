package query

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"arxivlens/internal/db"
)

//go:embed web/index.html
var indexHTML []byte

// The Server's dependencies are typed as the methods its handlers call, so the
// handlers can be tested with fakes (RET-1d). The concrete types are *SearchEngine,
// *RAGService, *db.DB and *PaperIngester.
type hybridSearcher interface {
	HybridSearch(ctx context.Context, queryString string, categories []string, limit int, rerank bool) ([]SearchHit, error)
}

type asker interface {
	AskWith(ctx context.Context, question string, categories []string, maxCitations int, opts AskOptions) (*AskResponse, error)
}

type statusStore interface {
	GetPaperStatus(ctx context.Context, arxivID string) (*db.PaperRecord, error)
	GetSystemStats(ctx context.Context) (*db.SystemStats, error)
}

type paperIngester interface {
	IngestPaper(ctx context.Context, input string) (*IngestResult, error)
	DiscoverAndIngest(ctx context.Context, query string) (*IngestResult, error)
}

type Server struct {
	searchEngine hybridSearcher
	ragService   asker
	database     statusStore
	ingester     paperIngester // nil interface when not configured (503 on ingest/discover)
	port         int
}

func NewServer(database *db.DB, searchEngine *SearchEngine, ragService *RAGService, port int) *Server {
	if port <= 0 {
		port = 8080
	}
	return &Server{
		searchEngine: searchEngine,
		ragService:   ragService,
		database:     database,
		port:         port,
	}
}

func (s *Server) SetIngester(ingester *PaperIngester) {
	// A nil *PaperIngester stored in the interface would not be == nil, and the
	// handlers' 503 "not configured" check would be skipped.
	if ingester == nil {
		s.ingester = nil
		return
	}
	s.ingester = ingester
}

type SearchRequest struct {
	Query      string   `json:"query"`
	Categories []string `json:"categories,omitempty"`
	Limit      int      `json:"limit,omitempty"`
	Rerank     bool     `json:"rerank,omitempty"`
}

type AskRequest struct {
	Question     string   `json:"question"`
	Categories   []string `json:"categories,omitempty"`
	MaxCitations int      `json:"max_citations,omitempty"`
	// Quick returns the quoted answer without LLM synthesis (first stage of a two-stage answer).
	Quick bool `json:"quick,omitempty"`
}

type IngestAPIRequest struct {
	ArxivID string `json:"arxiv_id"`
	URL     string `json:"url"`
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleHealth)
	mux.HandleFunc("GET /v1/stats", s.handleStats)
	mux.HandleFunc("POST /v1/search", s.handleSearch)
	mux.HandleFunc("POST /v1/ask", s.handleAsk)
	mux.HandleFunc("POST /v1/papers/ingest", s.handleIngestPaper)
	mux.HandleFunc("POST /v1/papers/discover", s.handleDiscoverPaper)
	mux.HandleFunc("GET /v1/papers/", s.handlePaperStatus)

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()

	log.Printf("[Query Service] HTTP API server listening on :%d", s.port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// writeJSON marshals v before touching headers or status, so an unencodable
// value (e.g. a NaN or ±Inf float64) becomes a logged 500 instead of a 200 with
// an empty or truncated body. On success the body is byte-identical to
// what a json.Encoder writes: HTML-escaped and followed by a newline.
func writeJSON(w http.ResponseWriter, status int, v any, route, traceID string) {
	body, err := json.Marshal(v)
	if err != nil {
		if traceID == "" {
			traceID = "-"
		}
		log.Printf("[Query Service] encode response failed route=%s trace_id=%s: %v", route, traceID, err)
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"}, "/healthz", "")
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req SearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request payload: %v", err), http.StatusBadRequest)
		return
	}

	if req.Query == "" {
		http.Error(w, "query is required", http.StatusBadRequest)
		return
	}

	hits, err := s.searchEngine.HybridSearch(r.Context(), req.Query, req.Categories, req.Limit, req.Rerank)
	if err != nil {
		http.Error(w, fmt.Sprintf("search failed: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"query": req.Query,
		"count": len(hits),
		"hits":  hits,
	}, "/v1/search", "")
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req AskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request payload: %v", err), http.StatusBadRequest)
		return
	}

	if req.Question == "" {
		http.Error(w, "question is required", http.StatusBadRequest)
		return
	}

	resp, err := s.ragService.AskWith(r.Context(), req.Question, req.Categories, req.MaxCitations, AskOptions{Quick: req.Quick})
	if err != nil {
		http.Error(w, fmt.Sprintf("rag ask failed: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, resp, "/v1/ask", resp.TraceID)
}

func (s *Server) handlePaperStatus(w http.ResponseWriter, r *http.Request) {
	// Path: /v1/papers/{arxiv_id}/status
	path := strings.TrimPrefix(r.URL.Path, "/v1/papers/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 || parts[0] == "" {
		http.Error(w, "arxiv_id is required", http.StatusBadRequest)
		return
	}
	arxivID := parts[0]

	paper, err := s.database.GetPaperStatus(r.Context(), arxivID)
	if err != nil {
		http.Error(w, fmt.Sprintf("paper not found: %v", err), http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, paper, "/v1/papers/{arxiv_id}/status", "")
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.database.GetSystemStats(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get stats: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, stats, "/v1/stats", "")
}

func (s *Server) handleIngestPaper(w http.ResponseWriter, r *http.Request) {
	var req IngestAPIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	input := strings.TrimSpace(req.ArxivID)
	if input == "" {
		input = strings.TrimSpace(req.URL)
	}
	if input == "" {
		http.Error(w, "arxiv_id or url is required", http.StatusBadRequest)
		return
	}

	if s.ingester == nil {
		http.Error(w, "paper ingester not configured on server", http.StatusServiceUnavailable)
		return
	}

	res, err := s.ingester.IngestPaper(r.Context(), input)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}, "/v1/papers/ingest", "")
		return
	}

	writeJSON(w, http.StatusOK, res, "/v1/papers/ingest", "")
}

type DiscoverAPIRequest struct {
	Query string `json:"query"`
}

func (s *Server) handleDiscoverPaper(w http.ResponseWriter, r *http.Request) {
	var req DiscoverAPIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	query := strings.TrimSpace(req.Query)
	if query == "" {
		http.Error(w, "query is required", http.StatusBadRequest)
		return
	}

	if s.ingester == nil {
		http.Error(w, "paper ingester not configured on server", http.StatusServiceUnavailable)
		return
	}

	res, err := s.ingester.DiscoverAndIngest(r.Context(), query)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}, "/v1/papers/discover", "")
		return
	}

	writeJSON(w, http.StatusOK, res, "/v1/papers/discover", "")
}
