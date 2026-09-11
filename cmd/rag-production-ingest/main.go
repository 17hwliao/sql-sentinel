package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"sqlsentinel/internal/rag"
)

func main() {
	manifestPath := flag.String("manifest", "docs/rag-corpus-manifest.json", "reviewed corpus manifest")
	documentID := flag.String("document-id", "", "allowlisted manifest document ID")
	source := flag.String("source", "", "local bytes for the selected manifest entry")
	flag.Parse()
	if *documentID == "" || *source == "" {
		fmt.Fprintln(os.Stderr, "usage: rag-production-ingest --document-id ID --source FILE [--manifest FILE]")
		os.Exit(2)
	}
	var manifest rag.CorpusManifest
	rawManifest, err := os.ReadFile(*manifestPath)
	if err != nil {
		fail(fmt.Errorf("read corpus manifest: %w", err))
	}
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		fail(fmt.Errorf("decode corpus manifest: %w", err))
	}
	var entry rag.ManifestEntry
	found := false
	for _, candidate := range manifest.Entries {
		if candidate.DocumentID == *documentID {
			entry, found = candidate, true
			break
		}
	}
	if !found {
		fail(fmt.Errorf("manifest document %q is not allowlisted", *documentID))
	}
	content, err := os.ReadFile(*source)
	if err != nil {
		fail(err)
	}
	config, err := rag.LoadProductionConfig(nil)
	if err != nil {
		fail(err)
	}
	service, _, err := rag.NewProductionService(context.Background(), config, rag.ChunkConfig{})
	if err != nil {
		fail(err)
	}
	document, chunks, err := service.IngestManifestEntry(context.Background(), manifest, entry, string(content), time.Time{})
	if err != nil {
		fail(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		Document rag.Document `json:"document"`
		Chunks   int          `json:"chunks"`
	}{Document: document, Chunks: len(chunks)})
}

func fail(err error) { fmt.Fprintln(os.Stderr, "rag-production-ingest:", err); os.Exit(1) }
