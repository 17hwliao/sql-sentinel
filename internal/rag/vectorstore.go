package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type memoryEntry struct {
	chunk   Chunk
	vector  []float64
	deleted bool
}

// MemoryVectorStore is the local adapter. It is process-local by design.
type MemoryVectorStore struct {
	mu      sync.RWMutex
	entries map[string]memoryEntry
	dim     int
}

func NewMemoryVectorStore(dimension int) (*MemoryVectorStore, error) {
	if dimension < 1 {
		return nil, fmt.Errorf("%w: vector dimension is required", ErrInvalidInput)
	}
	return &MemoryVectorStore{entries: make(map[string]memoryEntry), dim: dimension}, nil
}

func (s *MemoryVectorStore) Upsert(ctx context.Context, chunks []Chunk, vectors [][]float64) error {
	if len(chunks) != len(vectors) {
		return fmt.Errorf("%w: chunks and vectors length differ", ErrInvalidInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := chunk.Scope.validate(); err != nil || chunk.ID == "" || len(vectors[i]) != s.dim {
			return fmt.Errorf("%w: invalid chunk or vector at index %d", ErrInvalidInput, i)
		}
		for id, entry := range s.entries {
			if entry.chunk.Scope == chunk.Scope && entry.chunk.DocumentID == chunk.DocumentID && entry.chunk.Version != chunk.Version {
				delete(s.entries, id)
			}
		}
		s.entries[chunk.ID] = memoryEntry{chunk: chunk, vector: append([]float64(nil), vectors[i]...)}
	}
	return nil
}

func (s *MemoryVectorStore) Search(ctx context.Context, scope Scope, query []float64, options SearchOptions) ([]SearchHit, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	if len(query) != s.dim {
		return nil, fmt.Errorf("%w: query vector dimension mismatch", ErrInvalidInput)
	}
	if options.TopK <= 0 {
		options.TopK = DefaultTopK
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	s.mu.RLock()
	hits := make([]SearchHit, 0, len(s.entries))
	for _, entry := range s.entries {
		if entry.deleted || entry.chunk.Scope != scope || isExpired(entry.chunk.ExpiresAt, options.Now) {
			continue
		}
		score := cosine(query, entry.vector)
		if score >= options.MinScore {
			hits = append(hits, SearchHit{Chunk: entry.chunk, Score: score})
		}
	}
	s.mu.RUnlock()
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].Chunk.ID < hits[j].Chunk.ID
		}
		return hits[i].Score > hits[j].Score
	})
	if len(hits) > options.TopK {
		hits = hits[:options.TopK]
	}
	return hits, nil
}

func (s *MemoryVectorStore) DeleteDocument(ctx context.Context, scope Scope, documentID string) error {
	if err := scope.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(documentID) == "" {
		return fmt.Errorf("%w: document id is required", ErrInvalidInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for id, entry := range s.entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.chunk.Scope == scope && entry.chunk.DocumentID == documentID {
			delete(s.entries, id)
			found = true
		}
	}
	if !found {
		return ErrDocumentNotFound
	}
	return nil
}

func cosine(left, right []float64) float64 {
	var dot, leftNorm, rightNorm float64
	for i := range left {
		dot += left[i] * right[i]
		leftNorm += left[i] * left[i]
		rightNorm += right[i] * right[i]
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / (sqrt(leftNorm) * sqrt(rightNorm))
}

func isExpired(expiresAt, now time.Time) bool { return !expiresAt.IsZero() && !expiresAt.After(now) }

// HTTPVectorStore is a REST adapter for a real vector service. The endpoint
// and API key are mandatory; no ambient credentials or fake success are used.
type HTTPVectorStore struct {
	endpoint string
	apiKey   string
	client   *http.Client
}
type HTTPVectorStoreConfig struct {
	Endpoint string
	APIKey   string
	Client   *http.Client
}

func NewHTTPVectorStore(cfg HTTPVectorStoreConfig) (*HTTPVectorStore, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.APIKey) == "" {
		return nil, ErrProviderRefused
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPVectorStore{endpoint: strings.TrimRight(cfg.Endpoint, "/"), apiKey: cfg.APIKey, client: client}, nil
}

func (s *HTTPVectorStore) request(ctx context.Context, method, path string, in, out any) error {
	var body *bytes.Reader
	if in == nil {
		body = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.endpoint+path, body)
	if err != nil {
		return fmt.Errorf("create vector request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: vector store request: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: vector store returned HTTP %s", ErrUnavailable, resp.Status)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode vector store response: %w", err)
		}
	}
	return nil
}

func (s *HTTPVectorStore) Upsert(ctx context.Context, chunks []Chunk, vectors [][]float64) error {
	return s.request(ctx, http.MethodPost, "/upsert", struct {
		Chunks  []Chunk     `json:"chunks"`
		Vectors [][]float64 `json:"vectors"`
	}{chunks, vectors}, nil)
}

func (s *HTTPVectorStore) Search(ctx context.Context, scope Scope, query []float64, options SearchOptions) ([]SearchHit, error) {
	var out struct {
		Hits []SearchHit `json:"hits"`
	}
	path := "/search?tenant_id=" + url.QueryEscape(scope.TenantID) + "&project_id=" + url.QueryEscape(scope.ProjectID)
	err := s.request(ctx, http.MethodPost, path, struct {
		Query    []float64 `json:"query"`
		TopK     int       `json:"top_k"`
		MinScore float64   `json:"min_score"`
	}{query, options.TopK, options.MinScore}, &out)
	return out.Hits, err
}

func (s *HTTPVectorStore) DeleteDocument(ctx context.Context, scope Scope, documentID string) error {
	path := "/documents/" + url.PathEscape(scope.TenantID) + "/" + url.PathEscape(scope.ProjectID) + "/" + url.PathEscape(documentID)
	return s.request(ctx, http.MethodDelete, path, nil, nil)
}
