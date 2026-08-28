// Package pipeline composes existing read-only evidence stages without
// measuring performance or granting a performance claim.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"sqlsentinel/internal/diagnosis"
	"sqlsentinel/internal/plancompare"
	"sqlsentinel/internal/shadowgate"
	"sqlsentinel/internal/sqladmit"
)

const (
	StepSQLAdmit         = "sql_admit"
	StepCandidateExplain = "candidate_explain"
	StepPlanCompare      = "plan_compare"
	StepDiagnosis        = "diagnosis"

	AdmissionFile  = "sql_admission.json"
	CandidateFile  = "candidate_explain_report.json"
	ComparisonFile = "explain_comparison_report.json"
	DiagnosisFile  = "diagnostic_hypotheses.json"
	SummaryFile    = "pipeline_report.json"
)

type Artifact struct {
	Step        string `json:"step"`
	File        string `json:"file"`
	SHA256      string `json:"sha256"`
	DigestScope string `json:"digest_scope,omitempty"`
}

type Reason struct {
	Code string `json:"code"`
}

// Report is the pipeline-level L1 boundary. It describes what did run, not a
// performance outcome.
type Report struct {
	EvidenceLevel               string     `json:"evidence_level"`
	EligibleForPerformanceClaim bool       `json:"eligible_for_performance_claim"`
	CompletedSteps              []string   `json:"completed_steps"`
	StoppedStep                 string     `json:"stopped_step,omitempty"`
	RejectionReasons            []Reason   `json:"rejection_reasons"`
	Artifacts                   []Artifact `json:"artifacts"`
}

// ExplainRunner performs the existing candidate-only gate and returns its
// report plus the baseline EXPLAIN JSON used for comparison.
type ExplainRunner func(context.Context, string, []byte) (shadowgate.Report, []byte, error)

type Input struct {
	SQL           []byte
	CandidateSpec []byte
	OutputDir     string
	RunExplain    ExplainRunner
}

func Run(ctx context.Context, input Input) (Report, error) {
	if len(input.SQL) == 0 {
		return Report{}, errors.New("SQL input is required")
	}
	if len(input.CandidateSpec) == 0 {
		return Report{}, errors.New("CandidateSpec input is required")
	}
	if input.RunExplain == nil {
		return Report{}, errors.New("EXPLAIN runner is required")
	}
	if err := prepareOutputDir(input.OutputDir); err != nil {
		return Report{}, err
	}

	report := Report{
		EvidenceLevel:               "L1",
		EligibleForPerformanceClaim: false,
		CompletedSteps:              []string{},
		RejectionReasons:            []Reason{},
		Artifacts:                   []Artifact{},
	}

	admission := sqladmit.Admit(string(input.SQL))
	admission.InputSHA256 = digest(input.SQL)
	if _, err := writeStage(input.OutputDir, AdmissionFile, admission, StepSQLAdmit, &report); err != nil {
		return report, err
	}
	if !admission.Accepted {
		return stop(input.OutputDir, report, StepSQLAdmit, admission.ReasonCode, nil)
	}

	candidateReport, baselineRaw, err := input.RunExplain(ctx, string(input.SQL), input.CandidateSpec)
	if err != nil {
		return stop(input.OutputDir, report, StepCandidateExplain, "candidate_explain_rejected", err)
	}
	candidateReport.InputSQLSHA256 = digest(input.SQL)
	candidateReport.InputCandidateSpecSHA256 = digest(input.CandidateSpec)
	candidateBytes, err := writeStage(input.OutputDir, CandidateFile, candidateReport, StepCandidateExplain, &report)
	if err != nil {
		return report, err
	}

	comparison, err := plancompare.BuildReport(plancompare.Metadata{
		GeneratedAt:  candidateReport.GeneratedAt,
		MySQLVersion: candidateReport.MySQLVersion,
		Snapshot:     plancompare.Snapshot{Digest: candidateReport.Snapshot.Digest, Rows: candidateReport.Snapshot.Rows},
		SQL:          plancompare.SQLInput{SQL: candidateReport.SQL.SQL, Signals: candidateReport.SQL.Signals},
		CandidateIndex: plancompare.CandidateIndex{
			Table: candidateReport.Candidate.Table, Name: candidateReport.Candidate.Name,
			DDL: candidateReport.Candidate.DDL, Created: candidateReport.Candidate.Created,
		},
	}, baselineRaw, candidateReport.ExplainJSON)
	if err != nil {
		return stop(input.OutputDir, report, StepPlanCompare, "plan_compare_rejected", err)
	}
	comparison.InputCandidateExplainSHA256 = digest(candidateBytes)
	comparison.InputBaselineExplainSHA256 = digest(baselineRaw)
	comparisonBytes, err := writeStage(input.OutputDir, ComparisonFile, comparison, StepPlanCompare, &report)
	if err != nil {
		return report, err
	}

	diagnosisReport, err := diagnosis.Analyze(comparisonBytes)
	if err != nil {
		return stop(input.OutputDir, report, StepDiagnosis, "diagnosis_rejected", err)
	}
	if _, err := writeStage(input.OutputDir, DiagnosisFile, diagnosisReport, StepDiagnosis, &report); err != nil {
		return report, err
	}
	return finish(input.OutputDir, report)
}

func stop(outputDir string, report Report, step, code string, cause error) (Report, error) {
	report.StoppedStep = step
	report.RejectionReasons = append(report.RejectionReasons, Reason{Code: code})
	finished, err := finish(outputDir, report)
	if err != nil {
		return finished, err
	}
	if cause != nil {
		return finished, fmt.Errorf("%s: %w", step, cause)
	}
	return finished, fmt.Errorf("%s rejected: %s", step, code)
}

func finish(outputDir string, report Report) (Report, error) {
	report.Artifacts = append(report.Artifacts, Artifact{
		Step:        "pipeline_summary",
		File:        SummaryFile,
		DigestScope: "pipeline_report_with_own_sha256_blank",
	})
	self := len(report.Artifacts) - 1
	report.Artifacts[self].SHA256 = digest(canonicalSummary(report))
	if _, err := writeJSON(filepath.Join(outputDir, SummaryFile), report); err != nil {
		return report, fmt.Errorf("write pipeline summary: %w", err)
	}
	return report, nil
}

// canonicalSummary makes the self-reference auditable: the pipeline summary's
// digest covers its complete JSON representation except its own SHA field.
func canonicalSummary(report Report) []byte {
	copy := report
	copy.Artifacts = append([]Artifact(nil), report.Artifacts...)
	for i := range copy.Artifacts {
		if copy.Artifacts[i].Step == "pipeline_summary" {
			copy.Artifacts[i].SHA256 = ""
		}
	}
	data, err := json.MarshalIndent(copy, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("marshal pipeline summary: %v", err))
	}
	return append(data, '\n')
}

func writeStage[T any](outputDir, file string, value T, step string, report *Report) ([]byte, error) {
	bytes, err := writeJSON(filepath.Join(outputDir, file), value)
	if err != nil {
		return nil, fmt.Errorf("write %s: %w", step, err)
	}
	report.CompletedSteps = append(report.CompletedSteps, step)
	report.Artifacts = append(report.Artifacts, Artifact{Step: step, File: file, SHA256: digest(bytes)})
	return bytes, nil
}

func writeJSON(path string, value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, err
	}
	return data, nil
}

func prepareOutputDir(dir string) error {
	if dir == "" {
		return errors.New("output directory is required")
	}
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("output path is not a directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("output directory must be empty")
	}
	return nil
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
