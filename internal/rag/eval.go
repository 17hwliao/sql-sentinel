package rag

import (
	"context"
	"fmt"
	"sort"
)

type EvalCase struct {
	Name              string
	Scope             Scope
	Question          string
	ExpectedChunkIDs  []string
	ForbiddenChunkIDs []string
	WantRefusal       bool
}
type EvalResult struct {
	Name      string  `json:"name"`
	Passed    bool    `json:"passed"`
	RecallHit bool    `json:"recall_hit"`
	RecallAtK float64 `json:"recall_at_k"`
	Refused   bool    `json:"refused"`
	LeakFree  bool    `json:"leak_free"`
	Error     string  `json:"error,omitempty"`
}
type EvaluationReport struct {
	RecallAtK         float64      `json:"recall_at_k"`
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

func Evaluate(ctx context.Context, service *Service, cases []EvalCase, options SearchOptions) (EvaluationReport, error) {
	if service == nil {
		return EvaluationReport{}, fmt.Errorf("%w: service is required", ErrInvalidInput)
	}
	if len(cases) == 0 {
		return EvaluationReport{}, fmt.Errorf("%w: evaluation cases are required", ErrInvalidInput)
	}
	report := EvaluationReport{Cases: make([]EvalResult, 0, len(cases))}
	var recallTotal, refusalTotal, isolationTotal float64
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
		}
		if len(testCase.ExpectedChunkIDs) > 0 {
			retrieved := make([]SearchHit, 0, len(answer.Citations))
			for _, citation := range answer.Citations {
				retrieved = append(retrieved, SearchHit{Chunk: Chunk{ID: citation.ChunkID}})
			}
			result.RecallAtK = RecallAtK(retrieved, testCase.ExpectedChunkIDs, options.TopK)
			result.RecallHit = result.RecallAtK > 0
		}
		if len(testCase.ExpectedChunkIDs) > 0 {
			recallTotal += result.RecallAtK
		}
		if testCase.WantRefusal {
			refusalTotal += boolFloat(result.Refused)
		}
		if len(testCase.ForbiddenChunkIDs) > 0 {
			isolationCases++
			isolationTotal += boolFloat(result.LeakFree)
		}
		result.Passed = result.Error == "" && result.LeakFree && (len(testCase.ExpectedChunkIDs) == 0 || result.RecallAtK == 1) && (!testCase.WantRefusal || result.Refused)
		report.Cases = append(report.Cases, result)
	}
	report.RecallAtK = recallTotal / float64(countCasesWithExpected(cases))
	if n := countRefusalCases(cases); n > 0 {
		report.NoEvidenceRefusal = refusalTotal / float64(n)
	}
	if isolationCases > 0 {
		report.IsolationPassRate = isolationTotal / float64(isolationCases)
	}
	return report, nil
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
		if len(c.ExpectedChunkIDs) > 0 {
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
