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

type Server struct {
	searchEngine *SearchEngine
	ragService   *RAGService
	database     *db.DB
	ingester     *PaperIngester
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

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"query": req.Query,
		"count": len(hits),
		"hits":  hits,
	})
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

	resp, err := s.ragService.Ask(r.Context(), req.Question, req.Categories, req.MaxCitations)
	if err != nil {
		http.Error(w, fmt.Sprintf("rag ask failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
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

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(paper)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.database.GetSystemStats(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get stats: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stats)
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}
