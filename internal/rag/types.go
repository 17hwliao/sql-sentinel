// Package rag provides a bounded, evidence-first project knowledge assistant.
// It never executes or generates SQL/DDL and is deliberately outside the
// SQL Sentinel L0/L1/L2 admission and performance-qualification path.
package rag

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	SchemaVersion       = "rag.v1"
	DefaultChunkRunes   = 800
	DefaultOverlapRunes = 100
	DefaultTopK         = 5
	MaxDocumentBytes    = 2 << 20
)

var (
	ErrNoEvidence       = errors.New("rag: no trustworthy evidence")
	ErrSQLScope         = errors.New("rag: SQL and DDL are outside the knowledge assistant scope")
	ErrInvalidInput     = errors.New("rag: invalid input")
	ErrProviderRefused  = errors.New("rag: external provider is not explicitly configured")
	ErrUnavailable      = errors.New("rag: external dependency unavailable")
	ErrDocumentNotFound = errors.New("rag: document not found")
)

// Scope is part of every write and read. An empty project is not a wildcard.
type Scope struct {
	TenantID  string `json:"tenant_id"`
	ProjectID string `json:"project_id"`
}

func (s Scope) validate() error {
	if strings.TrimSpace(s.TenantID) == "" || strings.TrimSpace(s.ProjectID) == "" {
		return fmt.Errorf("%w: tenant_id and project_id are required", ErrInvalidInput)
	}
	return nil
}

type Document struct {
	ID          string    `json:"document_id"`
	Scope       Scope     `json:"scope"`
	SourceURI   string    `json:"source_uri"`
	Version     string    `json:"version"`
	ContentType string    `json:"content_type"`
	Content     string    `json:"-"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
}

type Chunk struct {
	ID             string    `json:"chunk_id"`
	DocumentID     string    `json:"document_id"`
	Scope          Scope     `json:"scope"`
	SourceURI      string    `json:"source_uri"`
	Version        string    `json:"version"`
	DocumentSHA256 string    `json:"document_sha256"`
	Ordinal        int       `json:"ordinal"`
	StartRune      int       `json:"start_rune"`
	EndRune        int       `json:"end_rune"`
	Text           string    `json:"text"`
	TextSHA256     string    `json:"text_sha256"`
	Untrusted      bool      `json:"untrusted"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
}

type IngestRequest struct {
	DocumentID  string
	Scope       Scope
	SourceURI   string
	Version     string
	ContentType string
	Content     string
	ExpiresAt   time.Time
}

type ChunkConfig struct {
	MaxRunes     int
	OverlapRunes int
}

func (c ChunkConfig) normalized() (ChunkConfig, error) {
	if c.MaxRunes == 0 {
		c.MaxRunes = DefaultChunkRunes
	}
	if c.OverlapRunes == 0 {
		c.OverlapRunes = DefaultOverlapRunes
	}
	if c.MaxRunes < 32 || c.MaxRunes > 16_000 || c.OverlapRunes < 0 || c.OverlapRunes >= c.MaxRunes {
		return ChunkConfig{}, fmt.Errorf("%w: chunk max must be 32..16000 and overlap must be smaller", ErrInvalidInput)
	}
	return c, nil
}

type SearchOptions struct {
	TopK     int
	MinScore float64
	Now      time.Time
	Reranker Reranker
}

type SearchHit struct {
	Chunk Chunk   `json:"chunk"`
	Score float64 `json:"score"`
}

type Citation struct {
	DocumentID     string  `json:"document_id"`
	ChunkID        string  `json:"chunk_id"`
	SourceURI      string  `json:"source_uri"`
	Version        string  `json:"version"`
	DocumentSHA256 string  `json:"document_sha256"`
	SHA256         string  `json:"sha256"`
	Score          float64 `json:"score"`
}

type ContextItem struct {
	Citation  Citation `json:"citation"`
	Text      string   `json:"text"`
	Untrusted bool     `json:"untrusted"`
}

// AnswerContext is deliberately context-only. A caller may pass it to a
// separate answerer, but the package provides no SQL/DDL generation method.
type AnswerContext struct {
	Question    string        `json:"question"`
	HasEvidence bool          `json:"has_evidence"`
	Refusal     string        `json:"refusal,omitempty"`
	Items       []ContextItem `json:"items"`
	Citations   []Citation    `json:"citations"`
	SystemRules []string      `json:"system_rules"`
}

type EmbeddingProvider interface {
	Embed(context.Context, []string) ([][]float64, error)
	Dimension() int
}

type VectorStore interface {
	Upsert(context.Context, []Chunk, [][]float64) error
	Search(context.Context, Scope, []float64, SearchOptions) ([]SearchHit, error)
	DeleteDocument(context.Context, Scope, string) error
}

type Reranker interface {
	Rerank(context.Context, string, []SearchHit) ([]SearchHit, error)
}

func validateQuestion(question string) error {
	q := strings.ToLower(strings.TrimSpace(question))
	if q == "" {
		return fmt.Errorf("%w: question is required", ErrInvalidInput)
	}
	// This is a safety boundary, not a SQL parser. It prevents the knowledge
	// assistant from being used as a disguised SQL/DDL generation endpoint.
	keywords := []string{"select ", "insert ", "update ", "delete ", "create table", "create index", "alter table", "drop table", "drop index", "truncate "}
	for _, keyword := range keywords {
		if strings.Contains(q, keyword) {
			return ErrSQLScope
		}
	}
	return nil
}
