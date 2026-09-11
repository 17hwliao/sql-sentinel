// rag-production-eval runs the checked-in golden retrieval cases against the
// configured real embedding endpoint and persistent Qdrant collection. It
// never asks an LLM to judge relevance: document IDs in the reviewed golden
// set are the metric oracle.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"sqlsentinel/internal/rag"
)

type goldenSet struct {
	Cases []goldenCase `json:"cases"`
}

type goldenCase struct {
	ID                   string    `json:"id"`
	Scope                rag.Scope `json:"scope"`
	Question             string    `json:"question"`
	RelevantDocumentIDs  []string  `json:"relevant_document_ids"`
	ForbiddenDocumentIDs []string  `json:"forbidden_document_ids"`
	WantRefusal          bool      `json:"want_refusal"`
	Fixture              string    `json:"fixture"`
}

func main() {
	goldenPath := flag.String("golden", "docs/rag-golden-set.json", "reviewed golden retrieval dataset")
	topK := flag.Int("top-k", rag.DefaultTopK, "retrieval count (1..16)")
	allowFixtureSkips := flag.Bool("allow-fixture-skips", false, "report rather than fail fixture-only cases; fixture cases must run in their isolated test harness")
	flag.Parse()
	if os.Getenv("RAG_REAL_SMOKE") != "1" {
		write(map[string]any{"status": "verification_unavailable", "code": "real_smoke_not_explicitly_enabled", "network_attempts": 0})
		os.Exit(1)
	}
	if *topK < 1 || *topK > 16 {
		fail("top-k must be between 1 and 16")
	}
	raw, err := os.ReadFile(*goldenPath)
	if err != nil {
		fail("read golden set: " + err.Error())
	}
	var golden goldenSet
	if err := json.Unmarshal(raw, &golden); err != nil || len(golden.Cases) == 0 {
		fail("decode golden set")
	}
	cases, skipped := make([]rag.EvalCase, 0, len(golden.Cases)), make([]string, 0)
	for _, item := range golden.Cases {
		if item.Fixture != "" {
			skipped = append(skipped, item.ID)
			continue
		}
		cases = append(cases, rag.EvalCase{Name: item.ID, Scope: item.Scope, Question: item.Question, ExpectedDocumentIDs: item.RelevantDocumentIDs, ForbiddenDocumentIDs: item.ForbiddenDocumentIDs, WantRefusal: item.WantRefusal})
	}
	if len(cases) == 0 {
		fail("golden set contains no production-safe cases")
	}
	config, err := rag.LoadProductionConfig(nil)
	if err != nil {
		write(map[string]any{"status": "verification_unavailable", "code": "production_configuration_missing", "network_attempts": 0})
		fail(err.Error())
	}
	service, _, err := rag.NewProductionService(context.Background(), config, rag.ChunkConfig{})
	if err != nil {
		write(map[string]any{"status": "verification_failed", "code": "dependency_unavailable"})
		fail(err.Error())
	}
	report, err := rag.Evaluate(context.Background(), service, cases, rag.SearchOptions{TopK: *topK})
	if err != nil {
		fail(err.Error())
	}
	passed := true
	for _, item := range report.Cases {
		passed = passed && item.Passed
	}
	status := "verification_completed"
	if len(skipped) > 0 {
		status = "verification_completed_with_fixture_skips"
		passed = passed && *allowFixtureSkips
	}
	write(map[string]any{"status": status, "passed": passed, "report": report, "fixture_cases_not_run": skipped})
	if !passed {
		os.Exit(1)
	}
}

func write(value any)     { _ = json.NewEncoder(os.Stdout).Encode(value) }
func fail(message string) { fmt.Fprintln(os.Stderr, "rag-production-eval:", message); os.Exit(1) }
