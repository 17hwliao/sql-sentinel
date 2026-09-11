package rag

import (
	"context"
	"fmt"
	"sort"
)

type EvalCase struct {
	Name                 string
	Scope                Scope
	Question             string
	ExpectedChunkIDs     []string
	ExpectedDocumentIDs  []string
	ForbiddenChunkIDs    []string
	ForbiddenDocumentIDs []string
	WantRefusal          bool
}
type EvalResult struct {
	Name              string  `json:"name"`
	Passed            bool    `json:"passed"`
	RecallAtK         float64 `json:"recall_at_k"`
	PrecisionAtK      float64 `json:"precision_at_k"`
	RelevantRetrieved int     `json:"relevant_retrieved"`
	Refused           bool    `json:"refused"`
	LeakFree          bool    `json:"leak_free"`
	Error             string  `json:"error,omitempty"`
}
type EvaluationReport struct {
	RecallAtK         float64      `json:"recall_at_k"`
	PrecisionAtK      float64      `json:"precision_at_k"`
	NoEvidenceRefusal float64      `json:"no_evidence_refusal_rate"`
	IsolationPassRate float64      `json:"isolation_pass_rate"`
	Cases             []EvalResult `json:"cases"`
}

func RecallAtK(retrieved []SearchHit, expected []string, k int) float64 {
	if len(expected) == 0 {
		return 1
	}
	if k <= 0 || k > len(retrieved) {
		k = len(retrieved)
	}
	wanted := make(map[string]struct{}, len(expected))
	for _, id := range expected {
		wanted[id] = struct{}{}
	}
	matched := make(map[string]struct{}, len(wanted))
	for _, hit := range retrieved[:k] {
		if _, ok := wanted[hit.Chunk.ID]; ok {
			matched[hit.Chunk.ID] = struct{}{}
		}
	}
	return float64(len(matched)) / float64(len(wanted))
}

// PrecisionAtK is the share of returned chunks that are relevant. It is zero
// when a query returns no chunks, rather than treating a refusal as precision.
func PrecisionAtK(retrieved []SearchHit, expected []string, k int) float64 {
	if k <= 0 || k > len(retrieved) {
		k = len(retrieved)
	}
	if k == 0 {
		return 0
	}
	wanted := make(map[string]struct{}, len(expected))
	for _, id := range expected {
		wanted[id] = struct{}{}
	}
	matched := 0
	for _, hit := range retrieved[:k] {
		if _, ok := wanted[hit.Chunk.ID]; ok {
			matched++
		}
	}
	return float64(matched) / float64(k)
}

func Evaluate(ctx context.Context, service *Service, cases []EvalCase, options SearchOptions) (EvaluationReport, error) {
	if service == nil {
		return EvaluationReport{}, fmt.Errorf("%w: service is required", ErrInvalidInput)
	}
	if len(cases) == 0 {
		return EvaluationReport{}, fmt.Errorf("%w: evaluation cases are required", ErrInvalidInput)
	}
	report := EvaluationReport{Cases: make([]EvalResult, 0, len(cases))}
	var recallTotal, precisionTotal, refusalTotal, isolationTotal float64
	isolationCases := 0
	for _, testCase := range cases {
		result := EvalResult{Name: testCase.Name, LeakFree: true}
		answer, err := service.Query(ctx, testCase.Scope, testCase.Question, options)
		result.Refused = err == ErrNoEvidence
		if err != nil && !result.Refused {
			result.Error = err.Error()
		}
		for _, citation := range answer.Citations {
			for _, forbidden := range testCase.ForbiddenChunkIDs {
				if citation.ChunkID == forbidden {
					result.LeakFree = false
				}
			}
			for _, forbidden := range testCase.ForbiddenDocumentIDs {
				if citation.DocumentID == forbidden {
					result.LeakFree = false
				}
			}
		}
		hits := make([]SearchHit, 0, len(answer.Citations))
		for _, citation := range answer.Citations {
			hits = append(hits, SearchHit{Chunk: Chunk{ID: citation.ChunkID, DocumentID: citation.DocumentID}})
		}
		if len(testCase.ExpectedChunkIDs) > 0 || len(testCase.ExpectedDocumentIDs) > 0 {
			result.RecallAtK, result.PrecisionAtK, result.RelevantRetrieved = caseMetrics(hits, testCase, options.TopK)
			recallTotal += result.RecallAtK
			precisionTotal += result.PrecisionAtK
		}
		if testCase.WantRefusal {
			refusalTotal += boolFloat(result.Refused)
		}
		if len(testCase.ForbiddenChunkIDs) > 0 || len(testCase.ForbiddenDocumentIDs) > 0 {
			isolationCases++
			isolationTotal += boolFloat(result.LeakFree)
		}
		result.Passed = result.Error == "" && result.LeakFree && (len(testCase.ExpectedChunkIDs) == 0 && len(testCase.ExpectedDocumentIDs) == 0 || result.RecallAtK == 1) && (!testCase.WantRefusal || result.Refused)
		report.Cases = append(report.Cases, result)
	}
	report.RecallAtK = recallTotal / float64(countCasesWithExpected(cases))
	report.PrecisionAtK = precisionTotal / float64(countCasesWithExpected(cases))
	if n := countRefusalCases(cases); n > 0 {
		report.NoEvidenceRefusal = refusalTotal / float64(n)
	}
	if isolationCases > 0 {
		report.IsolationPassRate = isolationTotal / float64(isolationCases)
	}
	return report, nil
}

func caseMetrics(hits []SearchHit, testCase EvalCase, k int) (recall, precision float64, relevant int) {
	if k <= 0 || k > len(hits) {
		k = len(hits)
	}
	chunkTargets := make(map[string]struct{}, len(testCase.ExpectedChunkIDs))
	documentTargets := make(map[string]struct{}, len(testCase.ExpectedDocumentIDs))
	for _, id := range testCase.ExpectedChunkIDs {
		chunkTargets[id] = struct{}{}
	}
	for _, id := range testCase.ExpectedDocumentIDs {
		documentTargets[id] = struct{}{}
	}
	matchedChunks, matchedDocuments := map[string]struct{}{}, map[string]struct{}{}
	for _, hit := range hits[:k] {
		matched := false
		if _, ok := chunkTargets[hit.Chunk.ID]; ok {
			matchedChunks[hit.Chunk.ID] = struct{}{}
			matched = true
		}
		if _, ok := documentTargets[hit.Chunk.DocumentID]; ok {
			matchedDocuments[hit.Chunk.DocumentID] = struct{}{}
			matched = true
		}
		if matched {
			relevant++
		}
	}
	targets := len(chunkTargets) + len(documentTargets)
	if targets > 0 {
		recall = float64(len(matchedChunks)+len(matchedDocuments)) / float64(targets)
	}
	if k > 0 {
		precision = float64(relevant) / float64(k)
	}
	return recall, precision, relevant
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
func countCasesWithExpected(cases []EvalCase) int {
	n := 0
	for _, c := range cases {
		if len(c.ExpectedChunkIDs) > 0 || len(c.ExpectedDocumentIDs) > 0 {
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return n
}
func countRefusalCases(cases []EvalCase) int {
	n := 0
	for _, c := range cases {
		if c.WantRefusal {
			n++
		}
	}
	return n
}
func StableCaseIDs(cases []EvalCase) []string {
	ids := make([]string, 0, len(cases))
	for _, item := range cases {
		ids = append(ids, item.Name)
	}
	sort.Strings(ids)
	return ids
}
