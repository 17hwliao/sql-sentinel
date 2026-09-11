package rag

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CorpusManifest is the reviewable allowlist for RAG source material. It is
// intentionally data-only: remote URLs, globs, prompts, and arbitrary paths
// are not a corpus ingestion API.
type CorpusManifest struct {
	SchemaVersion string          `json:"schema_version"`
	CorpusVersion string          `json:"corpus_version"`
	Entries       []ManifestEntry `json:"entries"`
}

type ManifestEntry struct {
	DocumentID  string `json:"document_id"`
	Scope       Scope  `json:"scope"`
	SourceURI   string `json:"source_uri"`
	Version     string `json:"version"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type"`
}

func (m CorpusManifest) Validate() error {
	if m.SchemaVersion != SchemaVersion || strings.TrimSpace(m.CorpusVersion) == "" || len(m.Entries) == 0 {
		return fmt.Errorf("%w: invalid corpus manifest header", ErrInvalidInput)
	}
	seen := make(map[string]struct{}, len(m.Entries))
	for _, entry := range m.Entries {
		if err := entry.Scope.validate(); err != nil || strings.TrimSpace(entry.DocumentID) == "" || strings.TrimSpace(entry.SourceURI) == "" || strings.TrimSpace(entry.Version) == "" || len(entry.SHA256) != 64 || !allowedContentTypes[strings.ToLower(entry.ContentType)] {
			return fmt.Errorf("%w: invalid corpus manifest entry", ErrInvalidInput)
		}
		key := entry.Scope.TenantID + "\x00" + entry.Scope.ProjectID + "\x00" + entry.DocumentID
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w: duplicate corpus document", ErrInvalidInput)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// IngestManifestEntry binds bytes to an allowlisted entry. The caller must
// obtain bytes through its own reviewed delivery mechanism; SHA mismatch is a
// hard failure before chunking, embeddings, or vector writes begin.
func (s *Service) IngestManifestEntry(ctx context.Context, manifest CorpusManifest, entry ManifestEntry, content string, expiresAt time.Time) (Document, []Chunk, error) {
	if err := manifest.Validate(); err != nil {
		return Document{}, nil, err
	}
	allowed := false
	for _, candidate := range manifest.Entries {
		if candidate == entry {
			allowed = true
			break
		}
	}
	if !allowed {
		return Document{}, nil, fmt.Errorf("%w: source absent from corpus manifest", ErrInvalidInput)
	}
	// The manifest hash binds the exact reviewed source bytes. Document SHA is
	// separately computed after newline normalization for deterministic chunks.
	if sha256Hex(content) != strings.ToLower(entry.SHA256) {
		return Document{}, nil, fmt.Errorf("%w: corpus source digest mismatch", ErrInvalidInput)
	}
	return s.Ingest(ctx, IngestRequest{DocumentID: entry.DocumentID, Scope: entry.Scope, SourceURI: entry.SourceURI, Version: entry.Version, ContentType: entry.ContentType, Content: content, ExpiresAt: expiresAt})
}
