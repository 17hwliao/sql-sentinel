// Package plancompare extracts a deliberately small, stable view from MySQL
// EXPLAIN FORMAT=JSON output. The raw plans remain in the report because this
// package is not a complete MySQL plan AST.
package plancompare

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"
)

type TableAccess struct {
	TableName           string   `json:"table_name"`
	AccessType          string   `json:"access_type,omitempty"`
	PossibleKeys        []string `json:"possible_keys,omitempty"`
	Key                 string   `json:"key,omitempty"`
	RowsExaminedPerScan *int64   `json:"rows_examined_per_scan,omitempty"`
	UsingIndex          bool     `json:"using_index"`
}

type Summary struct {
	Tables       []TableAccess `json:"tables"`
	UsesFilesort bool          `json:"uses_filesort"`
}

type Plan struct {
	Raw     json.RawMessage `json:"raw"`
	Summary Summary         `json:"summary"`
}

type Difference struct {
	BaselineUsesFilesort  bool     `json:"baseline_uses_filesort"`
	CandidateUsesFilesort bool     `json:"candidate_uses_filesort"`
	FilesortChanged       bool     `json:"filesort_changed"`
	BaselineKeys          []string `json:"baseline_keys"`
	CandidateKeys         []string `json:"candidate_keys"`
	CandidateOnlyKeys     []string `json:"candidate_only_keys"`
}

type Snapshot struct {
	Digest string `json:"digest"`
	Rows   int64  `json:"rows"`
}

type SQLInput struct {
	SQL     string   `json:"sql"`
	Signals []string `json:"signals"`
}

type CandidateIndex struct {
	Table   string `json:"table"`
	Name    string `json:"name"`
	DDL     string `json:"ddl"`
	Created bool   `json:"created"`
}

type Metadata struct {
	GeneratedAt    string
	MySQLVersion   string
	Snapshot       Snapshot
	SQL            SQLInput
	CandidateIndex CandidateIndex
}

// Report contains plan facts only. It intentionally has the same stable
// eligibility key as all other evidence reports, but it can never grant a
// performance claim because no timing has been collected.
type Report struct {
	GeneratedAt                 string         `json:"generated_at"`
	EvidenceLevel               string         `json:"evidence_level"`
	EligibleForPerformanceClaim bool           `json:"eligible_for_performance_claim"`
	MySQLVersion                string         `json:"mysql_version"`
	Snapshot                    Snapshot       `json:"snapshot"`
	SQL                         SQLInput       `json:"sql_input"`
	CandidateIndex              CandidateIndex `json:"candidate_index"`
	Baseline                    Plan           `json:"baseline"`
	Candidate                   Plan           `json:"candidate"`
	Difference                  Difference     `json:"difference"`
}

func BuildReport(meta Metadata, baselineRaw, candidateRaw []byte) (Report, error) {
	baseline, err := Parse(baselineRaw)
	if err != nil {
		return Report{}, fmt.Errorf("parse baseline EXPLAIN JSON: %w", err)
	}
	candidate, err := Parse(candidateRaw)
	if err != nil {
		return Report{}, fmt.Errorf("parse candidate EXPLAIN JSON: %w", err)
	}
	generatedAt := meta.GeneratedAt
	if generatedAt == "" {
		generatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return Report{
		GeneratedAt:                 generatedAt,
		EvidenceLevel:               "L1",
		EligibleForPerformanceClaim: false,
		MySQLVersion:                meta.MySQLVersion,
		Snapshot:                    meta.Snapshot,
		SQL:                         meta.SQL,
		CandidateIndex:              meta.CandidateIndex,
		Baseline:                    baseline,
		Candidate:                   candidate,
		Difference:                  Diff(baseline.Summary, candidate.Summary),
	}, nil
}

// Parse validates and preserves raw JSON while recursively extracting only
// documented table-access fields. Unknown plan nodes are intentionally ignored.
func Parse(raw []byte) (Plan, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return Plan{}, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err == nil {
		return Plan{}, fmt.Errorf("trailing JSON value")
	}
	summary := Summary{Tables: []TableAccess{}}
	walk(root, &summary)
	return Plan{Raw: append(json.RawMessage(nil), raw...), Summary: summary}, nil
}

func walk(node any, summary *Summary) {
	switch value := node.(type) {
	case map[string]any:
		if filesort, ok := value["using_filesort"].(bool); ok && filesort {
			summary.UsesFilesort = true
		}
		if tableName, ok := value["table_name"].(string); ok {
			summary.Tables = append(summary.Tables, TableAccess{
				TableName:           tableName,
				AccessType:          stringField(value, "access_type"),
				PossibleKeys:        stringsField(value, "possible_keys"),
				Key:                 stringField(value, "key"),
				RowsExaminedPerScan: int64Field(value, "rows_examined_per_scan"),
				UsingIndex:          boolField(value, "using_index"),
			})
		}
		for _, child := range value {
			walk(child, summary)
		}
	case []any:
		for _, child := range value {
			walk(child, summary)
		}
	}
}

func stringField(values map[string]any, key string) string {
	v, _ := values[key].(string)
	return v
}

func stringsField(values map[string]any, key string) []string {
	raw, ok := values[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		if s, ok := value.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func int64Field(values map[string]any, key string) *int64 {
	value, ok := values[key]
	if !ok {
		return nil
	}
	var text string
	switch v := value.(type) {
	case json.Number:
		text = string(v)
	case string:
		text = v
	default:
		return nil
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

func boolField(values map[string]any, key string) bool {
	v, _ := values[key].(bool)
	return v
}

func Diff(baseline, candidate Summary) Difference {
	baselineKeys := uniqueKeys(baseline.Tables)
	candidateKeys := uniqueKeys(candidate.Tables)
	baselineSet := make(map[string]struct{}, len(baselineKeys))
	for _, key := range baselineKeys {
		baselineSet[key] = struct{}{}
	}
	candidateOnly := make([]string, 0)
	for _, key := range candidateKeys {
		if _, ok := baselineSet[key]; !ok {
			candidateOnly = append(candidateOnly, key)
		}
	}
	return Difference{
		BaselineUsesFilesort:  baseline.UsesFilesort,
		CandidateUsesFilesort: candidate.UsesFilesort,
		FilesortChanged:       baseline.UsesFilesort != candidate.UsesFilesort,
		BaselineKeys:          baselineKeys,
		CandidateKeys:         candidateKeys,
		CandidateOnlyKeys:     candidateOnly,
	}
}

func uniqueKeys(tables []TableAccess) []string {
	seen := map[string]struct{}{}
	for _, table := range tables {
		if table.Key != "" {
			seen[table.Key] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for key := range seen {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func WriteJSON(w interface{ Write([]byte) (int, error) }, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
