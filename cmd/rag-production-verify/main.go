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
	question := flag.String("question", "What project knowledge is available?", "non-SQL smoke question")
	tenant := flag.String("tenant", "local", "tenant scope")
	project := flag.String("project", "sql-sentinel", "project scope")
	topK := flag.Int("top-k", 1, "number of retrieved chunks supplied to the answer model")
	flag.Parse()
	if *topK < 1 || *topK > rag.DefaultTopK {
		write(map[string]any{"status": "verification_unavailable", "code": "invalid_top_k", "network_attempts": 0})
		return
	}
	if os.Getenv("RAG_REAL_SMOKE") != "1" {
		write(map[string]any{"status": "verification_unavailable", "code": "real_smoke_not_explicitly_enabled", "network_attempts": 0})
		os.Exit(1)
	}
	config, err := rag.LoadProductionConfig(nil)
	if err != nil {
		write(map[string]any{"status": "verification_unavailable", "code": "production_configuration_missing", "network_attempts": 0})
		fail(err)
	}
	service, answerer, err := rag.NewProductionQueryService(context.Background(), config, rag.ChunkConfig{})
	if err != nil {
		write(map[string]any{"status": "verification_failed", "code": "dependency_unavailable"})
		fail(err)
	}
	answer, err := rag.QueryAndAnswer(context.Background(), service, answerer, rag.Scope{TenantID: *tenant, ProjectID: *project}, *question, rag.SearchOptions{TopK: *topK})
	if err != nil {
		write(map[string]any{"status": "verification_failed", "code": "query_or_answer_refused", "answer": answer})
		fail(err)
	}
	write(map[string]any{"status": "verification_completed", "answer": answer})
}

func write(value any) { _ = json.NewEncoder(os.Stdout).Encode(value) }
func fail(err error)  { fmt.Fprintln(os.Stderr, "rag-production-verify:", err); os.Exit(1) }
