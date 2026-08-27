// Package evidencebind prevents an L1 diagnostic hypothesis from being
// associated with unrelated benchmark evidence. All comparisons are exact and
// local; this package never runs a measurement or upgrades evidence itself.
package evidencebind

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"sqlsentinel/internal/diagnosis"
	"sqlsentinel/internal/plancompare"
	"sqlsentinel/internal/report"
	"sqlsentinel/internal/series"
)

const (
	ReasonDiagnosisSourceMismatch     = "diagnosis_source_sha_mismatch"
	ReasonDiagnosisSnapshotMismatch   = "diagnosis_snapshot_mismatch"
	ReasonDiagnosisMySQLMismatch      = "diagnosis_mysql_version_mismatch"
	ReasonMeasurementSnapshotMismatch = "measurement_snapshot_mismatch"
	ReasonMeasurementMySQLMismatch    = "measurement_mysql_version_mismatch"
	ReasonMeasurementSQLMismatch      = "measurement_sql_mismatch"
	ReasonCandidateIndexMismatch      = "candidate_index_mismatch"
	ReasonSeriesMeasurementMismatch   = "series_measurement_file_mismatch"
	ReasonSeriesNotEligible           = "series_not_eligible"
	ReasonSeriesEvidenceNotL2         = "series_evidence_level_not_l2"
)

type Reason struct {
	Code string `json:"code"`
}

type Source struct {
	DiagnosisSHA256  string `json:"diagnosis_sha256"`
	ComparisonSHA256 string `json:"comparison_sha256"`
	SnapshotDigest   string `json:"snapshot_digest"`
	MySQLVersion     string `json:"mysql_version"`
	MeasurementFile  string `json:"measurement_file"`
	CandidateIndex   string `json:"candidate_index"`
}

type HypothesisBinding struct {
	Code  string `json:"code"`
	Bound bool   `json:"bound"`
}

// Report inherits a performance claim only when exact identity checks pass and
// the pre-existing series report has already granted L2 eligibility.
type Report struct {
	GeneratedAt                 string              `json:"generated_at"`
	EvidenceLevel               string              `json:"evidence_level"`
	EligibleForPerformanceClaim bool                `json:"eligible_for_performance_claim"`
	BindingComplete             bool                `json:"binding_complete"`
	Source                      Source              `json:"source"`
	Hypotheses                  []HypothesisBinding `json:"hypotheses"`
	RejectionReasons            []Reason            `json:"rejection_reasons"`
}

func Analyze(diagnosisRaw, comparisonRaw, measurementRaw, seriesRaw []byte, measurementPath string) (Report, error) {
	diagnosisReport, err := decode[diagnosis.Report](diagnosisRaw, "diagnosis")
	if err != nil {
		return Report{}, err
	}
	comparison, err := decode[plancompare.Report](comparisonRaw, "EXPLAIN comparison")
	if err != nil {
		return Report{}, err
	}
	measurement, err := decode[report.Result](measurementRaw, "measurement")
	if err != nil {
		return Report{}, err
	}
	seriesReport, err := decode[series.Admission](seriesRaw, "series admission")
	if err != nil {
		return Report{}, err
	}
	return Bind(diagnosisReport, diagnosisRaw, comparison, comparisonRaw, measurement, seriesReport, measurementPath)
}

func Bind(
	d diagnosis.Report,
	diagnosisRaw []byte,
	c plancompare.Report,
	comparisonRaw []byte,
	m report.Result,
	s series.Admission,
	measurementPath string,
) (Report, error) {
	if d.EvidenceLevel != "L1" || d.EligibleForPerformanceClaim {
		return Report{}, errors.New("diagnosis must be unverified L1 evidence")
	}
	if c.EvidenceLevel != "L1" || c.EligibleForPerformanceClaim {
		return Report{}, errors.New("EXPLAIN comparison must be unverified L1 evidence")
	}
	if len(d.Hypotheses) == 0 {
		return Report{}, errors.New("diagnosis contains no hypotheses")
	}

	diagnosisDigest := digest(diagnosisRaw)
	comparisonDigest := digest(comparisonRaw)
	reasons := make([]Reason, 0, 10)
	if d.Source.SHA256 != comparisonDigest {
		reasons = append(reasons, Reason{Code: ReasonDiagnosisSourceMismatch})
	}
	if d.Source.Snapshot != c.Snapshot.Digest {
		reasons = append(reasons, Reason{Code: ReasonDiagnosisSnapshotMismatch})
	}
	if d.Source.MySQLVersion != c.MySQLVersion {
		reasons = append(reasons, Reason{Code: ReasonDiagnosisMySQLMismatch})
	}
	if m.Snapshot.Digest != c.Snapshot.Digest {
		reasons = append(reasons, Reason{Code: ReasonMeasurementSnapshotMismatch})
	}
	if m.Env.MySQL != c.MySQLVersion {
		reasons = append(reasons, Reason{Code: ReasonMeasurementMySQLMismatch})
	}
	if m.Case.SQL != c.SQL.SQL {
		reasons = append(reasons, Reason{Code: ReasonMeasurementSQLMismatch})
	}
	if m.CandidateIndex != c.CandidateIndex.Name {
		reasons = append(reasons, Reason{Code: ReasonCandidateIndexMismatch})
	}
	if filepath.Base(measurementPath) != filepath.Base(s.MeasurementFile) {
		reasons = append(reasons, Reason{Code: ReasonSeriesMeasurementMismatch})
	}
	identityComplete := len(reasons) == 0
	if !s.EligibleForPerformanceClaim {
		reasons = append(reasons, Reason{Code: ReasonSeriesNotEligible})
	}
	if s.EvidenceLevel != "L2" {
		reasons = append(reasons, Reason{Code: ReasonSeriesEvidenceNotL2})
	}
	eligible := identityComplete && s.EligibleForPerformanceClaim && s.EvidenceLevel == "L2"
	level := "L1"
	if eligible {
		level = "L2"
	}
	hypotheses := make([]HypothesisBinding, len(d.Hypotheses))
	for i, h := range d.Hypotheses {
		hypotheses[i] = HypothesisBinding{Code: h.Code, Bound: eligible}
	}
	return Report{
		GeneratedAt:                 time.Now().UTC().Format(time.RFC3339),
		EvidenceLevel:               level,
		EligibleForPerformanceClaim: eligible,
		BindingComplete:             identityComplete,
		Source: Source{
			DiagnosisSHA256: diagnosisDigest, ComparisonSHA256: comparisonDigest,
			SnapshotDigest: c.Snapshot.Digest, MySQLVersion: c.MySQLVersion,
			MeasurementFile: filepath.Base(measurementPath), CandidateIndex: c.CandidateIndex.Name,
		},
		Hypotheses:       hypotheses,
		RejectionReasons: reasons,
	}, nil
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func decode[T any](raw []byte, name string) (T, error) {
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

func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
