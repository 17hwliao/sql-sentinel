package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// FakeEmbedding is deterministic, offline, and suitable for tests/demos. It
// is intentionally not presented as production semantic quality.
type FakeEmbedding struct{ dimension int }

func NewFakeEmbedding(dimension int) (FakeEmbedding, error) {
	if dimension < 8 || dimension > 4096 {
		return FakeEmbedding{}, fmt.Errorf("%w: fake embedding dimension must be 8..4096", ErrInvalidInput)
	}
	return FakeEmbedding{dimension: dimension}, nil
}

func (f FakeEmbedding) Dimension() int { return f.dimension }

func (f FakeEmbedding) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.dimension == 0 {
		return nil, fmt.Errorf("%w: fake embedding is not configured", ErrInvalidInput)
	}
	result := make([][]float64, len(texts))
	for i, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vector := make([]float64, f.dimension)
		for _, token := range strings.Fields(strings.ToLower(text)) {
			digest := sha256Hex(token)
			for j := 0; j < 4; j++ {
				idx := int(uint16(digest[j*4])<<8|uint16(digest[j*4+1])) % f.dimension
				sign := 1.0
				if digest[j*4+2]%2 == 1 {
					sign = -1.0
				}
				vector[idx] += sign
			}
		}
		result[i] = normalizeVector(vector)
	}
	return result, nil
}

func normalizeVector(vector []float64) []float64 {
	var norm float64
	for _, value := range vector {
		norm += value * value
	}
	if norm == 0 {
		return vector
	}
	for i := range vector {
		vector[i] /= sqrt(norm)
	}
	return vector
}

func sqrt(value float64) float64 {
	guess := value
	if guess < 1 {
		guess = 1
	}
	for i := 0; i < 12; i++ {
		guess = (guess + value/guess) / 2
	}
	return guess
}

// HTTPEmbeddingProvider is an explicit OpenAI-compatible real adapter. It
// never reads credentials from ambient environment variables.
type HTTPEmbeddingProvider struct {
	endpoint  string
	apiKey    string
	model     string
	dimension int
	client    *http.Client
}

// OpenAI-compatible embedding endpoints vary in their maximum input batch
// size. Ten is accepted by DashScope text-embedding-v4 and is deliberately
// conservative for the generic adapter; callers still receive one vector per
// input in their original order.
const maxEmbeddingBatchSize = 10

type HTTPEmbeddingConfig struct {
	Endpoint  string
	APIKey    string
	Model     string
	Dimension int
	Client    *http.Client
}

func NewHTTPEmbeddingProvider(cfg HTTPEmbeddingConfig) (*HTTPEmbeddingProvider, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.Model) == "" {
		return nil, ErrProviderRefused
	}
	if cfg.Dimension < 1 || cfg.Dimension > 16_000 {
		return nil, fmt.Errorf("%w: provider dimension must be 1..16000", ErrInvalidInput)
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPEmbeddingProvider{endpoint: cfg.Endpoint, apiKey: cfg.APIKey, model: cfg.Model, dimension: cfg.Dimension, client: client}, nil
}

func (p *HTTPEmbeddingProvider) Dimension() int { return p.dimension }

func (p *HTTPEmbeddingProvider) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("%w: embedding inputs are required", ErrInvalidInput)
	}
	result := make([][]float64, len(texts))
	for start := 0; start < len(texts); start += maxEmbeddingBatchSize {
		end := start + maxEmbeddingBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch, err := p.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		copy(result[start:end], batch)
	}
	return result, nil
}

func (p *HTTPEmbeddingProvider) embedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	payload := struct {
		Model          string   `json:"model"`
		Input          []string `json:"input"`
		Dimensions     int      `json:"dimensions,omitempty"`
		EncodingFormat string   `json:"encoding_format"`
	}{Model: p.model, Input: texts, Dimensions: p.dimension, EncodingFormat: "float"}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create embedding request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: embedding request: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Surface a provider error code for operator diagnosis, but never echo an
		// upstream message because it can contain input text or implementation
		// details. Credentials are never read from a response body.
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 8<<10)).Decode(&failure)
		if strings.TrimSpace(failure.Error.Code) != "" {
			return nil, fmt.Errorf("%w: embedding provider returned HTTP %s code=%s", ErrUnavailable, resp.Status, failure.Error.Code)
		}
		return nil, fmt.Errorf("%w: embedding provider returned HTTP %s", ErrUnavailable, resp.Status)
	}
	var decoded struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(decoded.Data) != len(texts) {
		return nil, fmt.Errorf("%w: provider returned %d vectors for %d inputs", ErrUnavailable, len(decoded.Data), len(texts))
	}
	result := make([][]float64, len(texts))
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= len(result) || len(item.Embedding) != p.dimension {
			return nil, fmt.Errorf("%w: invalid embedding index or dimension", ErrUnavailable)
		}
		result[item.Index] = item.Embedding
	}
	return result, nil
}
