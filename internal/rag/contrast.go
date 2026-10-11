package rag

import (
	"context"
	"errors"
	"math"
	"strings"
)

type ContrastCase struct {
	Name             string
	Scope            Scope
	Question         string
	ExpectedChunkIDs []string
	IrrelevantHits   []SearchHit
}
type ContrastResult struct {
	Name         string  `json:"name"`
	Mode         string  `json:"mode"`
	HasEvidence  bool    `json:"has_evidence"`
	Recall       float64 `json:"recall"`
	AnswerStatus string  `json:"answer_status"`
	Correct      *bool   `json:"correct,omitempty"`
}
type ContrastReport struct {
	Scope string           `json:"scope"`
	Cases []ContrastResult `json:"cases"`
}
type ContrastAnswerer func(context.Context, AnswerContext) (string, error)
type ContrastGrader func(ContrastCase, string) bool

// EvaluateContrast runs three evidence conditions with the same question. It
// makes no implicit model calls. Optional answer generation requires both an
// explicit answerer and a caller-owned grader; retrieval alone is not quality.
func EvaluateContrast(ctx context.Context, service *Service, cases []ContrastCase, options SearchOptions, answer ContrastAnswerer, grade ContrastGrader) (ContrastReport, error) {
	if service == nil || len(cases) == 0 || len(cases) > 100 || (answer == nil) != (grade == nil) {
		return ContrastReport{}, ErrInvalidInput
	}
	report := ContrastReport{Scope: "controlled_evidence_conditions; quality depends on supplied answerer/grader", Cases: []ContrastResult{}}
	names := map[string]bool{}
	for _, c := range cases {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if c.Name == "" || names[c.Name] || len(c.ExpectedChunkIDs) == 0 || len(c.ExpectedChunkIDs) > 1000 || len(c.IrrelevantHits) == 0 || len(c.IrrelevantHits) > 20 {
			return report, ErrInvalidInput
		}
		names[c.Name] = true
		expectedIDs := map[string]bool{}
		for _, id := range c.ExpectedChunkIDs {
			if id == "" || expectedIDs[id] {
				return report, ErrInvalidInput
			}
			expectedIDs[id] = true
		}
		retrieved, err := service.Query(ctx, c.Scope, c.Question, options)
		if err != nil && !errors.Is(err, ErrNoEvidence) {
			return report, err
		}
		var irrelevant []SearchHit
		now := options.Now
		if now.IsZero() {
			now = service.now()
		}
		irrelevantIDs := map[string]bool{}
		for _, hit := range c.IrrelevantHits {
			if hit.Chunk.ID == "" || strings.TrimSpace(hit.Chunk.Text) == "" || irrelevantIDs[hit.Chunk.ID] || expectedIDs[hit.Chunk.ID] || hit.Chunk.Scope != c.Scope || !hit.Chunk.Untrusted || isExpired(hit.Chunk.ExpiresAt, now) || math.IsNaN(hit.Score) || math.IsInf(hit.Score, 0) {
				return report, ErrInvalidInput
			}
			irrelevantIDs[hit.Chunk.ID] = true
			irrelevant = append(irrelevant, hit)
		}
		conditions := []struct {
			mode    string
			context AnswerContext
		}{{"without_context", ContextFromHits(c.Question, nil)}, {"retrieved_context", retrieved}, {"irrelevant_context", ContextFromHits(c.Question, irrelevant)}}
		for _, condition := range conditions {
			result := ContrastResult{Name: c.Name, Mode: condition.mode, HasEvidence: condition.context.HasEvidence, AnswerStatus: "skipped_no_answerer"}
			var hits []SearchHit
			for _, citation := range condition.context.Citations {
				hits = append(hits, SearchHit{Chunk: Chunk{ID: citation.ChunkID}})
			}
			result.Recall = RecallAtK(hits, c.ExpectedChunkIDs, len(hits))
			if answer != nil {
				response, e := answer(ctx, condition.context)
				if e != nil {
					return report, e
				}
				correct := grade(c, response)
				result.Correct = &correct
				result.AnswerStatus = "evaluated_by_supplied_grader"
			}
			report.Cases = append(report.Cases, result)
		}
	}
	return report, nil
}
