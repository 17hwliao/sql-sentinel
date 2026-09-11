package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"sqlsentinel/internal/rag"
)

type output struct {
	Document rag.Document      `json:"document"`
	Chunks   []rag.Chunk       `json:"chunks"`
	Context  rag.AnswerContext `json:"answer_context,omitempty"`
	Refusal  string            `json:"refusal,omitempty"`
}

func main() {
	in := flag.String("in", "", "controlled Markdown/text document")
	question := flag.String("question", "", "project knowledge question; SQL/DDL is refused")
	tenant := flag.String("tenant", "local", "tenant scope")
	project := flag.String("project", "sql-sentinel", "project scope")
	flag.Parse()
	if *in == "" || *question == "" {
		fmt.Fprintln(os.Stderr, "usage: go run ./cmd/rag-demo --in FILE --question QUESTION")
		os.Exit(2)
	}
	input, err := os.Open(*in)
	if err != nil {
		fail(err)
	}
	defer input.Close()
	embedding, err := rag.NewFakeEmbedding(128)
	if err != nil {
		fail(err)
	}
	store, err := rag.NewMemoryVectorStore(128)
	if err != nil {
		fail(err)
	}
	service, err := rag.NewService(embedding, store, rag.ChunkConfig{})
	if err != nil {
		fail(err)
	}
	content, err := io.ReadAll(input)
	if err != nil {
		fail(err)
	}
	doc, chunks, err := service.Ingest(context.Background(), rag.IngestRequest{DocumentID: *in, Scope: rag.Scope{TenantID: *tenant, ProjectID: *project}, SourceURI: "file://" + *in, Version: time.Now().UTC().Format("20060102T150405Z"), ContentType: "text/markdown", Content: string(content)})
	if err != nil {
		fail(err)
	}
	answer, queryErr := service.Query(context.Background(), rag.Scope{TenantID: *tenant, ProjectID: *project}, *question, rag.SearchOptions{TopK: rag.DefaultTopK, Reranker: rag.LexicalReranker{}})
	result := output{Document: doc, Chunks: chunks, Context: answer}
	if queryErr != nil {
		result.Refusal = queryErr.Error()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fail(err)
	}
}

func fail(err error) { fmt.Fprintln(os.Stderr, "rag-demo:", err); os.Exit(1) }
