package rag

import (
	"context"
	"strings"
	"testing"
)

func TestEvidenceContrastDoesNotInventModelQuality(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	scope := Scope{"local", "knowledge"}
	_, chunks, err := s.Ingest(ctx, IngestRequest{DocumentID: "retention", Scope: scope, SourceURI: "docs://retention", Version: "v1", ContentType: "text/plain", Content: "retention policy retains reports for 48 hours"})
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := s.Ingest(ctx, IngestRequest{DocumentID: "unrelated", Scope: scope, SourceURI: "docs://unrelated", Version: "v1", ContentType: "text/plain", Content: "billing currency is CNY"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []ContrastCase{{Name: "retention", Scope: scope, Question: "retention policy", ExpectedChunkIDs: []string{chunks[0].ID}, IrrelevantHits: []SearchHit{{Chunk: other[0], Score: 1}}}}
	options := SearchOptions{TopK: 1, Reranker: LexicalReranker{}}
	report, err := EvaluateContrast(ctx, s, cases, options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Cases) != 3 || report.Cases[0].HasEvidence || report.Cases[1].Recall != 1 || report.Cases[2].Recall != 0 {
		t.Fatal("evidence conditions incorrect", report)
	}
	for _, c := range report.Cases {
		if c.Correct != nil || c.AnswerStatus != "skipped_no_answerer" {
			t.Fatal("retrieval presented as model quality")
		}
	}
	for _, invalid := range []ContrastCase{
		{Name: "empty-control", Scope: scope, Question: "retention", ExpectedChunkIDs: []string{chunks[0].ID}, IrrelevantHits: []SearchHit{{Chunk: Chunk{ID: "empty", Scope: scope, Untrusted: true}, Score: 1}}},
		{Name: "missing-irrelevant", Scope: scope, Question: "retention", ExpectedChunkIDs: []string{chunks[0].ID}},
		{Name: "duplicate-labels", Scope: scope, Question: "retention", ExpectedChunkIDs: []string{chunks[0].ID, chunks[0].ID}, IrrelevantHits: cases[0].IrrelevantHits},
		{Name: "relevant-as-irrelevant", Scope: scope, Question: "retention", ExpectedChunkIDs: []string{chunks[0].ID}, IrrelevantHits: []SearchHit{{Chunk: chunks[0], Score: 1}}},
	} {
		if _, err = EvaluateContrast(ctx, s, []ContrastCase{invalid}, options, nil, nil); err == nil {
			t.Fatal("invalid evidence control accepted", invalid.Name)
		}
	}
	// This scripted fixture validates evaluator wiring, not a live model score.
	answer := func(_ context.Context, a AnswerContext) (string, error) {
		for _, item := range a.Items {
			if strings.Contains(item.Text, "48 hours") {
				return "48 hours", nil
			}
		}
		return "insufficient evidence", nil
	}
	grade := func(_ ContrastCase, a string) bool { return a == "48 hours" }
	report, err = EvaluateContrast(ctx, s, cases, options, answer, grade)
	if err != nil || !*report.Cases[1].Correct || *report.Cases[0].Correct || *report.Cases[2].Correct {
		t.Fatal(report, err)
	}
	cases[0].IrrelevantHits[0].Chunk.Scope = Scope{"other", "knowledge"}
	if _, err = EvaluateContrast(ctx, s, cases, options, answer, grade); err == nil {
		t.Fatal("contrast widened tenant scope")
	}
}
