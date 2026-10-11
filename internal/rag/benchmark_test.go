package rag

import (
	"context"
	"fmt"
	"testing"
)

// In-memory fake embeddings measure only bounded retrieval mechanics.
func BenchmarkScopedRetrieval(b *testing.B) {
	embedding, _ := NewFakeEmbedding(128)
	store, _ := NewMemoryVectorStore(128)
	service, err := NewService(embedding, store, ChunkConfig{})
	if err != nil {
		b.Fatal(err)
	}
	scope := Scope{"benchmark", "knowledge"}
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		_, _, err = service.Ingest(ctx, IngestRequest{DocumentID: fmt.Sprintf("doc-%d", i), Scope: scope, SourceURI: "fixture://benchmark", Version: "v1", ContentType: "text/plain", Content: fmt.Sprintf("project retention policy retains reports for %d hours", i+1)})
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err = service.Query(ctx, scope, "retention policy", SearchOptions{TopK: 5, Reranker: LexicalReranker{}}); err != nil {
			b.Fatal(err)
		}
	}
}
