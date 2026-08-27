// Package diagnosis turns an existing L1 EXPLAIN comparison into explicitly
// unverified hypotheses. It never connects to MySQL and never recommends or
// applies an index.
package diagnosis

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"sqlsentinel/internal/plancompare"
)

var (
	ErrEvidenceLevel       = errors.New("diagnosis requires an L1 EXPLAIN comparison report")
	ErrPerformanceEligible = errors.New("diagnosis input must not be eligible for a performance claim")
)

const (
	CodeFilesortRemoved      = "candidate_index_removes_filesort"
	CodeAccessPathChanged    = "candidate_index_changes_access_path"
	CodeEstimatedScanReduced = "candidate_index_reduces_estimated_scan"
)

type Source struct {
	SHA256       string `json:"sha256"`
	GeneratedAt  string `json:"generated_at"`
	MySQLVersion string `json:"mysql_version"`
	Snapshot     string `json:"snapshot_digest"`
}

type Fact struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

type Hypothesis struct {
	Code                 string   `json:"code"`
	EvidenceLevel        string   `json:"evidence_level"`
	Statement            string   `json:"statement"`
	Facts                []Fact   `json:"facts"`
	RequiredVerification []string `json:"required_verification"`
}

// Report deliberately does not contain a recommendation. Hypotheses are only
// prompts for the pre-existing controlled measurement and series-admission
// evidence chain.
type Report struct {
	GeneratedAt                 string       `json:"generated_at"`
	EvidenceLevel               string       `json:"evidence_level"`
	EligibleForPerformanceClaim bool         `json:"eligible_for_performance_claim"`
	Source                      Source       `json:"source"`
	Hypotheses                  []Hypothesis `json:"hypotheses"`
	Unresolved                  []string     `json:"unresolved"`
}

func Analyze(raw []byte) (Report, error) {
	input, err := decodeStrict(raw)
	if err != nil {
		return Report{}, err
	}
	if input.EvidenceLevel != "L1" {
		return Report{}, fmt.Errorf("%w: got %q", ErrEvidenceLevel, input.EvidenceLevel)
	}
	if input.EligibleForPerformanceClaim {
		return Report{}, ErrPerformanceEligible
	}
	if strings.TrimSpace(input.SQL.SQL) == "" || input.Snapshot.Digest == "" {
		return Report{}, errors.New("comparison report lacks SQL or snapshot evidence")
	}

	sum := sha256.Sum256(raw)
	hypotheses := derive(input)
	return Report{
		GeneratedAt:                 time.Now().UTC().Format(time.RFC3339),
		EvidenceLevel:               "L1",
		EligibleForPerformanceClaim: false,
		Source: Source{
			SHA256:       hex.EncodeToString(sum[:]),
			GeneratedAt:  input.GeneratedAt,
			MySQLVersion: input.MySQLVersion,
			Snapshot:     input.Snapshot.Digest,
		},
		Hypotheses: hypotheses,
		Unresolved: []string{
			"EXPLAIN differences are plan evidence, not measured latency or write-cost evidence.",
			"Each hypothesis requires controlled_ab_measurement and series_admission before a performance claim.",
		},
	}, nil
}

func decodeStrict(raw []byte) (plancompare.Report, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var out plancompare.Report
	if err := dec.Decode(&out); err != nil {
		return plancompare.Report{}, fmt.Errorf("decode EXPLAIN comparison: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return plancompare.Report{}, errors.New("decode EXPLAIN comparison: trailing JSON value")
		}
		return plancompare.Report{}, fmt.Errorf("decode EXPLAIN comparison trailing value: %w", err)
	}
	return out, nil
}

func derive(input plancompare.Report) []Hypothesis {
	out := make([]Hypothesis, 0, 3)
	diff := input.Difference
	if diff.BaselineUsesFilesort && !diff.CandidateUsesFilesort {
		out = append(out, hypothesis(CodeFilesortRemoved,
			"The candidate plan removes a filesort that is present in the baseline plan.", []Fact{
				{Field: "difference.baseline_uses_filesort", Value: "true"},
				{Field: "difference.candidate_uses_filesort", Value: "false"},
			}))
	}
	if len(diff.CandidateOnlyKeys) > 0 {
		out = append(out, hypothesis(CodeAccessPathChanged,
			"The candidate plan uses an index that is absent from the baseline plan.", []Fact{
				{Field: "difference.candidate_only_keys", Value: strings.Join(diff.CandidateOnlyKeys, ",")},
			}))
	}
	if baselineRows, candidateRows, ok := comparableEstimatedRows(input.Baseline.Summary, input.Candidate.Summary); ok && candidateRows < baselineRows {
		out = append(out, hypothesis(CodeEstimatedScanReduced,
			"The candidate plan estimates fewer rows examined than the baseline plan.", []Fact{
				{Field: "baseline.rows_examined_per_scan_total", Value: fmt.Sprint(baselineRows)},
				{Field: "candidate.rows_examined_per_scan_total", Value: fmt.Sprint(candidateRows)},
			}))
	}
	return out
}

func hypothesis(code, statement string, facts []Fact) Hypothesis {
	return Hypothesis{
		Code:          code,
		EvidenceLevel: "L1",
		Statement:     statement,
		Facts:         facts,
		RequiredVerification: []string{
			"controlled_ab_measurement",
			"series_admission",
		},
	}
}

// comparableEstimatedRows only compares a one-to-one ordered table sequence
// with complete estimates. Unknown or differently shaped plans yield no claim.
func comparableEstimatedRows(baseline, candidate plancompare.Summary) (int64, int64, bool) {
	if len(baseline.Tables) == 0 || len(baseline.Tables) != len(candidate.Tables) {
		return 0, 0, false
	}
	var bTotal, cTotal int64
	for i := range baseline.Tables {
		b, c := baseline.Tables[i], candidate.Tables[i]
		if b.TableName != c.TableName || b.RowsExaminedPerScan == nil || c.RowsExaminedPerScan == nil {
			return 0, 0, false
		}
		bTotal += *b.RowsExaminedPerScan
		cTotal += *c.RowsExaminedPerScan
	}
	return bTotal, cTotal, true
}

func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
