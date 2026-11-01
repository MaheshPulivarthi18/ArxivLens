package query

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type EmbedClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewEmbedClient(baseURL string) *EmbedClient {
	if baseURL == "" {
		baseURL = "http://localhost:8081"
	}
	return &EmbedClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

type EmbedRequest struct {
	Texts   []string `json:"texts"`
	IsQuery bool     `json:"is_query"`
}

type EmbedResponse struct {
	Vectors [][]float32 `json:"vectors"`
	Dim     int         `json:"dim"`
}

type RerankRequest struct {
	Query    string   `json:"query"`
	Passages []string `json:"passages"`
}

type RerankResponse struct {
	Scores []float32 `json:"scores"`
}

func (c *EmbedClient) EmbedTexts(ctx context.Context, texts []string, isQuery bool) ([][]float32, error) {
	reqBody, err := json.Marshal(EmbedRequest{
		Texts:   texts,
		IsQuery: isQuery,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embed request: %w", err)
	}

	url := fmt.Sprintf("%s/internal/embed", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embed service returned status %d: %s", resp.StatusCode, string(body))
	}

	var res EmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("failed to decode embed response: %w", err)
	}

	return res.Vectors, nil
}

func (c *EmbedClient) Rerank(ctx context.Context, query string, passages []string) ([]float32, error) {
	reqBody, err := json.Marshal(RerankRequest{
		Query:    query,
		Passages: passages,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal rerank request: %w", err)
	}

	url := fmt.Sprintf("%s/internal/rerank", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create rerank request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("rerank service returned status %d: %s", resp.StatusCode, string(body))
	}

	var res RerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("failed to decode rerank response: %w", err)
	}

	return res.Scores, nil
}
