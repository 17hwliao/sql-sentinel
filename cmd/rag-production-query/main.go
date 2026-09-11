package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"sqlsentinel/internal/rag"
)

func main() {
	question := flag.String("question", "", "project knowledge question; SQL/DDL is refused")
	tenant := flag.String("tenant", "local", "tenant scope")
	project := flag.String("project", "sql-sentinel", "project scope")
	topK := flag.Int("top-k", rag.DefaultTopK, "retrieval count (1..16)")
	flag.Parse()
	if *question == "" || *topK < 1 || *topK > 16 {
		fmt.Fprintln(os.Stderr, "usage: rag-production-query --question QUESTION [--tenant ID --project ID --top-k 1..16]")
		os.Exit(2)
	}
	ctx := context.Background()
	config, err := rag.LoadProductionConfig(nil)
	if err != nil {
		write(rag.GroundedAnswer{RefusalCode: "production_configuration_missing"})
		fail(err)
	}
	service, answerer, err := rag.NewProductionQueryService(ctx, config, rag.ChunkConfig{})
	if err != nil {
		write(rag.GroundedAnswer{RefusalCode: "production_dependency_unavailable"})
		fail(err)
	}
	answer, err := rag.QueryAndAnswer(ctx, service, answerer, rag.Scope{TenantID: *tenant, ProjectID: *project}, *question, rag.SearchOptions{TopK: *topK})
	write(answer)
	if err != nil {
		fail(err)
	}
}

func write(value rag.GroundedAnswer) { _ = json.NewEncoder(os.Stdout).Encode(value) }
func fail(err error)                 { fmt.Fprintln(os.Stderr, "rag-production-query:", err); os.Exit(1) }
