package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

var allowedContentTypes = map[string]bool{
	"text/plain":      true,
	"text/markdown":   true,
	"text/x-markdown": true,
}

func IngestDocument(req IngestRequest, cfg ChunkConfig, now time.Time) (Document, []Chunk, error) {
	if err := req.Scope.validate(); err != nil {
		return Document{}, nil, err
	}
	if strings.TrimSpace(req.DocumentID) == "" || strings.TrimSpace(req.Version) == "" || strings.TrimSpace(req.SourceURI) == "" {
		return Document{}, nil, fmt.Errorf("%w: document_id, version, and source_uri are required", ErrInvalidInput)
	}
	if !utf8.ValidString(req.Content) {
		return Document{}, nil, fmt.Errorf("%w: content must be valid UTF-8", ErrInvalidInput)
	}
	contentType := strings.ToLower(strings.TrimSpace(req.ContentType))
	if !allowedContentTypes[contentType] {
		return Document{}, nil, fmt.Errorf("%w: unsupported content type %q", ErrInvalidInput, req.ContentType)
	}
	if len([]byte(req.Content)) == 0 || len([]byte(req.Content)) > MaxDocumentBytes {
		return Document{}, nil, fmt.Errorf("%w: document must be 1..%d bytes", ErrInvalidInput, MaxDocumentBytes)
	}
	cfg, err := cfg.normalized()
	if err != nil {
		return Document{}, nil, err
	}
	content := normalizeContent(req.Content)
	if strings.TrimSpace(content) == "" {
		return Document{}, nil, fmt.Errorf("%w: document has no non-whitespace content", ErrInvalidInput)
	}
	digest := sha256Hex(content)
	doc := Document{
		ID: req.DocumentID, Scope: req.Scope, SourceURI: req.SourceURI,
		Version: req.Version, ContentType: contentType, Content: content,
		SHA256: digest, CreatedAt: now.UTC(), ExpiresAt: req.ExpiresAt,
	}
	return doc, chunkDocument(doc, cfg), nil
}

func ReadAndIngest(r io.Reader, req IngestRequest, cfg ChunkConfig, now time.Time) (Document, []Chunk, error) {
	if r == nil {
		return Document{}, nil, fmt.Errorf("%w: reader is required", ErrInvalidInput)
	}
	limited, err := io.ReadAll(io.LimitReader(r, MaxDocumentBytes+1))
	if err != nil {
		return Document{}, nil, fmt.Errorf("read document: %w", err)
	}
	req.Content = string(limited)
	return IngestDocument(req, cfg, now)
}

func normalizeContent(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	return strings.TrimSpace(content) + "\n"
}

func chunkDocument(doc Document, cfg ChunkConfig) []Chunk {
	runes := []rune(doc.Content)
	chunks := make([]Chunk, 0, (len(runes)/cfg.MaxRunes)+1)
	start := 0
	ordinal := 0
	for start < len(runes) {
		end := start + cfg.MaxRunes
		if end > len(runes) {
			end = len(runes)
		}
		// Prefer a deterministic paragraph/line boundary when it leaves a
		// meaningful chunk. This makes Markdown chunks easier to cite while
		// keeping the hard rune bound explicit.
		if end < len(runes) {
			for candidate := end; candidate > start+cfg.MaxRunes/2; candidate-- {
				if runes[candidate-1] == '\n' {
					end = candidate
					break
				}
			}
		}
		text := strings.TrimSpace(string(runes[start:end]))
		if text != "" {
			// This ID is also the storage key for the local vector adapter. Bind
			// scope into it so identical documents in two tenants cannot overwrite
			// each other's evidence before scope filtering happens at query time.
			chunkID := sha256Hex(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%s", doc.Scope.TenantID, doc.Scope.ProjectID, doc.ID, doc.Version, ordinal, text))[:32]
			chunks = append(chunks, Chunk{
				ID: chunkID, DocumentID: doc.ID, Scope: doc.Scope, SourceURI: doc.SourceURI,
				Version: doc.Version, DocumentSHA256: doc.SHA256, Ordinal: ordinal, StartRune: start, EndRune: end,
				Text: text, TextSHA256: sha256Hex(text), Untrusted: true, ExpiresAt: doc.ExpiresAt,
			})
			ordinal++
		}
		if end == len(runes) {
			break
		}
		next := end - cfg.OverlapRunes
		if next <= start {
			next = end
		}
		start = next
	}
	return chunks
}

func sha256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
