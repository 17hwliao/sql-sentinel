// Package evaluation builds the final acceptance report from strict, local
// evidence. It reports scope limits rather than inferring missing evidence.
package evaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"sqlsentinel/internal/pipeline"
	"sqlsentinel/internal/report"
	"sqlsentinel/internal/sqladmit"
)

const (
	EvidenceLevelL1  = "L1"
	StatusMeasured   = "measured"
	StatusWaived     = "waived"
	StatusOutOfScope = "out_of_scope"
	StatusNotRun     = "not_run"
)

type Manifest struct {
	DatasetVersion string `json:"dataset_version"`
	Cases          []Case `json:"cases"`
}

type Case struct {
	ID              string `json:"id"`
	SQLFile         string `json:"sql_file"`
	ExpectSignal    string `json:"expect_signal,omitempty"`
	ExpectRejection string `json:"expect_rejection,omitempty"`
}

type StaticCaseResult struct {
	ID        string `json:"id"`
	Detected  bool   `json:"detected"`
	Signal    string `json:"signal,omitempty"`
	Rejection string `json:"rejection_code,omitempty"`
}

type StaticRules struct {
	Status         string             `json:"status"`
	DatasetVersion string             `json:"dataset_version"`
	ManifestSHA256 string             `json:"manifest_sha256"`
	Detected       int                `json:"detected"`
	Total          int                `json:"total"`
	Recall         float64            `json:"recall"`
	Cases          []StaticCaseResult `json:"cases"`
	Scope          string             `json:"scope"`
}

type Validation struct {
	Status                         string  `json:"status"`
	SourceSHA256                   string  `json:"source_sha256"`
	Rows                           int64   `json:"rows"`
	DatasetVersion                 string  `json:"dataset_version"`
	CaseName                       string  `json:"case_name"`
	DirectionVerdict               string  `json:"direction_verdict"`
	CandidateCalibrationNoiseRatio float64 `json:"candidate_calibration_noise_ratio"`
	EvidenceLevel                  string  `json:"evidence_level"`
	EligibleForPerformanceClaim    bool    `json:"eligible_for_performance_claim"`
	Limitation                     string  `json:"limitation"`
}

type Waiver struct {
	Dimension string `json:"dimension"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
}

type SecurityCheck struct {
	Name    string `json:"name"`
	Package string `json:"package"`
	Status  string `json:"status"`
}

type Security struct {
	Status string          `json:"status"`
	Checks []SecurityCheck `json:"checks"`
}

type Cost struct {
	Status         string         `json:"status"`
	LLMAttempts    int            `json:"llm_real_attempts"`
	TokenUsage     Waiver         `json:"token_usage"`
	PipelineTiming PipelineTiming `json:"pipeline_timing"`
}

type Environment struct {
	GitCommit            string `json:"git_commit"`
	EvaluatedAt          string `json:"evaluated_at"`
	WorkingTreeDirty     bool   `json:"working_tree_dirty"`
	SampleDatasetVersion string `json:"sample_dataset_version"`
	SampleManifestSHA256 string `json:"sample_manifest_sha256"`
}

type PipelineTiming struct {
	Status       string                  `json:"status"`
	SourceSHA256 string                  `json:"source_sha256,omitempty"`
	Steps        []pipeline.StepDuration `json:"steps,omitempty"`
	Artifacts    []pipeline.Artifact     `json:"artifacts,omitempty"`
}

type Report struct {
	EvidenceLevel               string      `json:"evidence_level"`
	EligibleForPerformanceClaim bool        `json:"eligible_for_performance_claim"`
	StaticRules                 StaticRules `json:"static_rules"`
	Validation                  Validation  `json:"validation_pass_rate"`
	Security                    Security    `json:"security"`
	Cost                        Cost        `json:"cost"`
	Waivers                     []Waiver    `json:"waivers"`
	Environment                 Environment `json:"environment"`
}

// SetEnvironment adds the execution facts collected by the local CLI. It does
// not infer a clean checkout when evaluating a working tree with pending work.
func SetEnvironment(report *Report, gitCommit, evaluatedAt string, dirty bool) {
	report.Environment = Environment{
		GitCommit: gitCommit, EvaluatedAt: evaluatedAt, WorkingTreeDirty: dirty,
		SampleDatasetVersion: report.StaticRules.DatasetVersion, SampleManifestSHA256: report.StaticRules.ManifestSHA256,
	}
}

type Input struct {
	ManifestPath   string
	ValidationPath string
}

func Build(input Input) (Report, error) {
	manifestRaw, err := os.ReadFile(input.ManifestPath)
	if err != nil {
		return Report{}, fmt.Errorf("read evaluation manifest: %w", err)
	}
	manifest, err := decodeStrict[Manifest](manifestRaw, "evaluation manifest")
	if err != nil {
		return Report{}, err
	}
	static, err := evaluateStatic(filepath.Dir(input.ManifestPath), manifest, digest(manifestRaw))
	if err != nil {
		return Report{}, err
	}
	validation, err := readValidation(input.ValidationPath)
	if err != nil {
		return Report{}, err
	}
	return Report{
		EvidenceLevel:               EvidenceLevelL1,
		EligibleForPerformanceClaim: false,
		StaticRules:                 static,
		Validation:                  validation,
		Security:                    DefaultSecurityChecklist(),
		Cost: Cost{Status: "partially_measured", LLMAttempts: 0, PipelineTiming: PipelineTiming{Status: StatusNotRun},
			TokenUsage: Waiver{Dimension: "token_usage", Status: StatusWaived, Reason: "token usage is not instrumented"}},
		Waivers: []Waiver{
			{Dimension: "optimality_proximity", Status: StatusWaived, Reason: "no proposal-to-A/B feedback iteration exists"},
			{Dimension: "result_equivalence", Status: StatusOutOfScope, Reason: "this project has no SQL rewrite capability"},
		},
	}, nil
}

// AttachPipelineTiming records the strict pipeline report emitted by the
// current smoke run. Its timing is operational wall-clock evidence only.
func AttachPipelineTiming(report *Report, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read pipeline timing report: %w", err)
	}
	pipelineReport, err := decodeStrict[pipeline.Report](raw, "pipeline timing report")
	if err != nil {
		return err
	}
	if pipelineReport.EvidenceLevel != EvidenceLevelL1 || pipelineReport.EligibleForPerformanceClaim || len(pipelineReport.StepDurations) == 0 {
		return errors.New("pipeline timing report is not an unverified L1 run with step durations")
	}
	report.Cost.PipelineTiming = PipelineTiming{
		Status: StatusMeasured, SourceSHA256: digest(raw), Steps: append([]pipeline.StepDuration(nil), pipelineReport.StepDurations...),
		Artifacts: append([]pipeline.Artifact(nil), pipelineReport.Artifacts...),
	}
	return nil
}

// RunSecurityChecks runs a fixed checklist through an injected local runner.
// Packages are constants, never request-controlled command arguments.
func RunSecurityChecks(run func(string) error) Security {
	checks := securityChecklist(StatusNotRun)
	allPassed := true
	for i := range checks {
		if err := run(checks[i].Package); err != nil {
			checks[i].Status = "failed"
			allPassed = false
			continue
		}
		checks[i].Status = "passed"
	}
	status := "passed"
	if !allPassed {
		status = "failed"
	}
	return Security{Status: status, Checks: checks}
}

func DefaultSecurityChecklist() Security {
	return Security{Status: StatusNotRun, Checks: securityChecklist(StatusNotRun)}
}

func evaluateStatic(root string, manifest Manifest, manifestSHA string) (StaticRules, error) {
	if strings.TrimSpace(manifest.DatasetVersion) == "" || len(manifest.Cases) == 0 {
		return StaticRules{}, errors.New("evaluation manifest requires dataset_version and cases")
	}
	result := StaticRules{
		Status: StatusMeasured, DatasetVersion: manifest.DatasetVersion, ManifestSHA256: manifestSHA,
		Cases: []StaticCaseResult{}, Scope: "curated known-smell SQL samples only; not a production SQL corpus",
	}
	seen := map[string]struct{}{}
	for _, c := range manifest.Cases {
		if c.ID == "" || !safeBaseName(c.SQLFile) || (c.ExpectSignal == "") == (c.ExpectRejection == "") {
			return StaticRules{}, fmt.Errorf("invalid evaluation case %q", c.ID)
		}
		if _, ok := seen[c.ID]; ok {
			return StaticRules{}, fmt.Errorf("duplicate evaluation case %q", c.ID)
		}
		seen[c.ID] = struct{}{}
		sql, err := os.ReadFile(filepath.Join(root, c.SQLFile))
		if err != nil {
			return StaticRules{}, fmt.Errorf("read evaluation SQL %q: %w", c.ID, err)
		}
		admission := sqladmit.Admit(string(sql))
		caseResult := StaticCaseResult{ID: c.ID, Rejection: admission.ReasonCode}
		for _, signal := range admission.Signals {
			if signal == c.ExpectSignal {
				caseResult.Signal = signal
			}
		}
		caseResult.Detected = caseResult.Signal != "" || caseResult.Rejection == c.ExpectRejection
		if caseResult.Detected {
			result.Detected++
		}
		result.Cases = append(result.Cases, caseResult)
	}
	result.Total = len(result.Cases)
	result.Recall = float64(result.Detected) / float64(result.Total)
	return result, nil
}

func readValidation(path string) (Validation, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Validation{}, fmt.Errorf("read validation report: %w", err)
	}
	measured, err := decodeStrict[report.Result](raw, "validation report")
	if err != nil {
		return Validation{}, err
	}
	if measured.Verdict == nil || measured.Measurement == nil || measured.Dataset.Rows <= 0 {
		return Validation{}, errors.New("validation report lacks a completed measurement")
	}
	return Validation{
		Status: StatusMeasured, SourceSHA256: digest(raw), Rows: measured.Dataset.Rows,
		DatasetVersion: measured.Dataset.DatasetVersion, CaseName: measured.Case.Name,
		DirectionVerdict:               measured.Verdict.Verdict,
		CandidateCalibrationNoiseRatio: measured.Calibration.Candidate.NoiseRatio,
		EvidenceLevel:                  EvidenceLevelL1, EligibleForPerformanceClaim: false,
		Limitation: "one historical query case; candidate calibration noise exceeds the 10% admission limit, so no performance claim is eligible",
	}, nil
}

func securityChecklist(status string) []SecurityCheck {
	return []SecurityCheck{
		{Name: "candidate_spec_strict_decode", Package: "./internal/candidate/...", Status: status},
		{Name: "agent_prompt_injection", Package: "./internal/agentgraph/...", Status: status},
		{Name: "readonly_sql_locking_read", Package: "./internal/sqladmit/...", Status: status},
		{Name: "webhook_signature_queue_and_degrade", Package: "./internal/webhook/...", Status: status},
		{Name: "pipeline_stop_and_partial_evidence", Package: "./internal/pipeline/...", Status: status},
	}
}

func WriteJSON(w io.Writer, report Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func decodeStrict[T any](raw []byte, name string) (T, error) {
	var out T
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("decode %s: %w", name, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return out, fmt.Errorf("decode %s: trailing JSON value", name)
		}
		return out, fmt.Errorf("decode %s trailing value: %w", name, err)
	}
	return out, nil
}

func safeBaseName(name string) bool {
	return name != "" && filepath.Base(name) == name && name != "." && name != ".."
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
