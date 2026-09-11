package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// QdrantVectorStore is the production, persistent VectorStore adapter. It
// stores tenant/project/document/version metadata with every point and never
// relies on a collection name as an authorization boundary.
type QdrantVectorStore struct {
	endpoint   string
	apiKey     string
	collection string
	dimension  int
	client     *http.Client
}

type QdrantConfig struct {
	Endpoint   string
	APIKey     string
	Collection string
	Dimension  int
	Client     *http.Client
}

var qdrantCollectionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)

func NewQdrantVectorStore(cfg QdrantConfig) (*QdrantVectorStore, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.Collection) == "" {
		return nil, ErrProviderRefused
	}
	if !qdrantCollectionName.MatchString(cfg.Collection) || cfg.Dimension < 1 || cfg.Dimension > 16_000 {
		return nil, fmt.Errorf("%w: invalid qdrant collection or dimension", ErrInvalidInput)
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("%w: invalid qdrant endpoint", ErrInvalidInput)
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &QdrantVectorStore{endpoint: strings.TrimRight(cfg.Endpoint, "/"), apiKey: cfg.APIKey, collection: cfg.Collection, dimension: cfg.Dimension, client: client}, nil
}

// Health only verifies that Qdrant's own health endpoint responds. It does
// not claim that an embedding provider, collection, or retrieval path works.
func (s *QdrantVectorStore) Health(ctx context.Context) error {
	return s.request(ctx, http.MethodGet, "/healthz", nil, nil)
}

// EnsureCollection is explicit so an unavailable server is surfaced at deploy
// time, instead of being silently masked by a process-local fallback.
func (s *QdrantVectorStore) EnsureCollection(ctx context.Context) error {
	if err := s.request(ctx, http.MethodPut, "/collections/"+url.PathEscape(s.collection), struct {
		Vectors struct {
			Size     int    `json:"size"`
			Distance string `json:"distance"`
		} `json:"vectors"`
	}{Vectors: struct {
		Size     int    `json:"size"`
		Distance string `json:"distance"`
	}{Size: s.dimension, Distance: "Cosine"}}, nil); err != nil {
		var conflict *qdrantStatusError
		if !errors.As(err, &conflict) || conflict.StatusCode != http.StatusConflict {
			return err
		}
		if err := s.validateExistingCollection(ctx); err != nil {
			return err
		}
	}
	// Payload indexes make the mandatory scope/document lifecycle filters
	// efficient, but are not used as a substitute for service-side checks.
	for _, field := range []string{"tenant_id", "project_id", "chunk.document_id"} {
		if err := s.request(ctx, http.MethodPut, "/collections/"+url.PathEscape(s.collection)+"/index", struct {
			FieldName   string `json:"field_name"`
			FieldSchema string `json:"field_schema"`
		}{FieldName: field, FieldSchema: "keyword"}, nil); err != nil {
			return err
		}
	}
	return nil
}

// validateExistingCollection makes startup idempotent without accepting an
// incompatible collection. A same-named collection with a different vector
// schema is a deployment error, never a reason to write malformed points.
func (s *QdrantVectorStore) validateExistingCollection(ctx context.Context) error {
	var response struct {
		Result struct {
			Config struct {
				Params struct {
					Vectors struct {
						Size     int    `json:"size"`
						Distance string `json:"distance"`
					} `json:"vectors"`
				} `json:"params"`
			} `json:"config"`
		} `json:"result"`
	}
	if err := s.request(ctx, http.MethodGet, "/collections/"+url.PathEscape(s.collection), nil, &response); err != nil {
		return err
	}
	vectors := response.Result.Config.Params.Vectors
	if vectors.Size != s.dimension || !strings.EqualFold(vectors.Distance, "Cosine") {
		return fmt.Errorf("%w: existing qdrant collection %q has dimension=%d distance=%q, want dimension=%d distance=%q", ErrProviderRefused, s.collection, vectors.Size, vectors.Distance, s.dimension, "Cosine")
	}
	return nil
}

type qdrantStatusError struct {
	StatusCode int
	Status     string
}

func (e *qdrantStatusError) Error() string { return "qdrant returned HTTP " + e.Status }
func (e *qdrantStatusError) Unwrap() error { return ErrUnavailable }

func (s *QdrantVectorStore) request(ctx context.Context, method, path string, in, out any) error {
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
		return fmt.Errorf("create qdrant request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.apiKey != "" {
		req.Header.Set("api-key", s.apiKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: qdrant request: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &qdrantStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode qdrant response: %w", err)
		}
	}
	return nil
}

func (s *QdrantVectorStore) Upsert(ctx context.Context, chunks []Chunk, vectors [][]float64) error {
	if len(chunks) == 0 || len(chunks) != len(vectors) {
		return fmt.Errorf("%w: qdrant chunks and vectors length differ or are empty", ErrInvalidInput)
	}
	first := chunks[0]
	if err := first.Scope.validate(); err != nil || first.DocumentID == "" || first.Version == "" {
		return fmt.Errorf("%w: invalid qdrant document metadata", ErrInvalidInput)
	}
	for i, chunk := range chunks {
		if chunk.Scope != first.Scope || chunk.DocumentID != first.DocumentID || chunk.Version != first.Version || len(vectors[i]) != s.dimension {
			return fmt.Errorf("%w: inconsistent qdrant batch", ErrInvalidInput)
		}
	}
	// A document version is atomic at the logical store boundary: remove every
	// older point for that scoped document before writing the new version.
	if err := s.deleteByFilter(ctx, first.Scope, first.DocumentID); err != nil {
		return err
	}
	type point struct {
		ID      string    `json:"id"`
		Vector  []float64 `json:"vector"`
		Payload any       `json:"payload"`
	}
	points := make([]point, 0, len(chunks))
	for i, chunk := range chunks {
		points = append(points, point{ID: qdrantPointID(chunk.ID), Vector: vectors[i], Payload: struct {
			Chunk     Chunk  `json:"chunk"`
			TenantID  string `json:"tenant_id"`
			ProjectID string `json:"project_id"`
		}{Chunk: chunk, TenantID: chunk.Scope.TenantID, ProjectID: chunk.Scope.ProjectID}})
	}
	return s.request(ctx, http.MethodPut, "/collections/"+url.PathEscape(s.collection)+"/points?wait=true", struct {
		Points []point `json:"points"`
	}{Points: points}, nil)
}

func (s *QdrantVectorStore) Search(ctx context.Context, scope Scope, query []float64, options SearchOptions) ([]SearchHit, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	if len(query) != s.dimension {
		return nil, fmt.Errorf("%w: qdrant query vector dimension mismatch", ErrInvalidInput)
	}
	if options.TopK <= 0 {
		options.TopK = DefaultTopK
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	type condition struct {
		Key   string `json:"key"`
		Match struct {
			Value string `json:"value"`
		} `json:"match"`
	}
	newCondition := func(key, value string) condition {
		var c condition
		c.Key, c.Match.Value = key, value
		return c
	}
	var response struct {
		Result struct {
			Points []struct {
				Score   float64         `json:"score"`
				Payload json.RawMessage `json:"payload"`
			} `json:"points"`
		} `json:"result"`
	}
	// Fetch extra candidates because expiry is checked again locally. This
	// avoids relying solely on remote filter semantics for a safety property.
	err := s.request(ctx, http.MethodPost, "/collections/"+url.PathEscape(s.collection)+"/points/query", struct {
		Query       []float64 `json:"query"`
		Limit       int       `json:"limit"`
		WithPayload bool      `json:"with_payload"`
		Filter      struct {
			Must []condition `json:"must"`
		} `json:"filter"`
	}{Query: query, Limit: options.TopK*5 + 20, WithPayload: true, Filter: struct {
		Must []condition `json:"must"`
	}{Must: []condition{newCondition("tenant_id", scope.TenantID), newCondition("project_id", scope.ProjectID)}}}, &response)
	if err != nil {
		return nil, err
	}
	hits := make([]SearchHit, 0, len(response.Result.Points))
	for _, point := range response.Result.Points {
		var payload struct {
			Chunk Chunk `json:"chunk"`
		}
		if err := json.Unmarshal(point.Payload, &payload); err != nil {
			return nil, fmt.Errorf("%w: invalid qdrant payload", ErrUnavailable)
		}
		if payload.Chunk.Scope == scope && payload.Chunk.Untrusted && !isExpired(payload.Chunk.ExpiresAt, options.Now) && point.Score >= options.MinScore {
			hits = append(hits, SearchHit{Chunk: payload.Chunk, Score: point.Score})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > options.TopK {
		hits = hits[:options.TopK]
	}
	return hits, nil
}

func (s *QdrantVectorStore) DeleteDocument(ctx context.Context, scope Scope, documentID string) error {
	if err := scope.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(documentID) == "" {
		return fmt.Errorf("%w: document id is required", ErrInvalidInput)
	}
	return s.deleteByFilter(ctx, scope, documentID)
}

func (s *QdrantVectorStore) deleteByFilter(ctx context.Context, scope Scope, documentID string) error {
	type condition struct {
		Key   string `json:"key"`
		Match struct {
			Value string `json:"value"`
		} `json:"match"`
	}
	newCondition := func(key, value string) condition {
		var c condition
		c.Key, c.Match.Value = key, value
		return c
	}
	return s.request(ctx, http.MethodPost, "/collections/"+url.PathEscape(s.collection)+"/points/delete?wait=true", struct {
		Filter struct {
			Must []condition `json:"must"`
		} `json:"filter"`
	}{Filter: struct {
		Must []condition `json:"must"`
	}{Must: []condition{newCondition("tenant_id", scope.TenantID), newCondition("project_id", scope.ProjectID), newCondition("chunk.document_id", documentID)}}}, nil)
}

// Qdrant accepts UUID point IDs. Chunk IDs are stable hex SHA-256 prefixes,
// so this deterministic conversion preserves idempotency without exposing a
// mutable database ID to callers.
func qdrantPointID(id string) string {
	clean := strings.ReplaceAll(strings.ToLower(id), "-", "")
	if len(clean) < 32 {
		clean += strings.Repeat("0", 32-len(clean))
	}
	clean = clean[:32]
	return clean[:8] + "-" + clean[8:12] + "-" + clean[12:16] + "-" + clean[16:20] + "-" + clean[20:]
}
