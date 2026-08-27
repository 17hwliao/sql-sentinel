// Package shadowgate turns a previously admitted SQL file and a constrained
// CandidateSpec into a candidate-shadow EXPLAIN report. It never executes the
// SQL file itself and never receives a writable baseline connection.
package shadowgate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"sqlsentinel/internal/candidate"
	"sqlsentinel/internal/measure"
	"sqlsentinel/internal/snapshot"
	"sqlsentinel/internal/sqladmit"
)

var (
	ErrSQLRejected             = errors.New("SQL does not pass read-only admission")
	ErrPlaceholderUnsupported  = errors.New("SQL bind placeholders are not supported")
	ErrUnsupportedTable        = errors.New("CandidateSpec table is outside the orders-v1 shadow scope")
	ErrTableMissing            = errors.New("CandidateSpec table does not exist on candidate")
	ErrColumnMissing           = errors.New("CandidateSpec column does not exist on candidate")
	ErrIndexDefinitionConflict = errors.New("candidate index name is already used by a different definition")
)

// Prepared is the database-independent result of the two input admission
// steps. It intentionally contains compiled DDL rather than accepting raw DDL.
type Prepared struct {
	SQL        string
	SQLSignals []string
	Spec       candidate.Spec
	DDL        string
}

// Prepare performs all checks possible before any database connection is made.
func Prepare(sql string, spec candidate.Spec) (Prepared, error) {
	admission := sqladmit.Admit(sql)
	if !admission.Accepted {
		return Prepared{}, fmt.Errorf("%w: %s", ErrSQLRejected, admission.ReasonCode)
	}
	if hasBindPlaceholder(sql) {
		return Prepared{}, ErrPlaceholderUnsupported
	}
	if spec.Table != "orders" {
		return Prepared{}, fmt.Errorf("%w: %q", ErrUnsupportedTable, spec.Table)
	}
	ddl, err := candidate.CompileCreateIndex(spec)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{SQL: sql, SQLSignals: admission.Signals, Spec: spec, DDL: ddl}, nil
}

// Gate has read access to baseline and candidate; only Candidate is used for
// DDL, ANALYZE and EXPLAIN. The fields make that write boundary explicit.
type Gate struct {
	Baseline  *sql.DB
	Candidate *sql.DB
	Chunk     int
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

// Report deliberately has no performance verdict. EXPLAIN plus schema is L1
// plan evidence, not a benchmark result.
type Report struct {
	GeneratedAt              string          `json:"generated_at"`
	EvidenceLevel            string          `json:"evidence_level"`
	PerformanceClaimEligible bool            `json:"performance_claim_eligible"`
	MySQLVersion             string          `json:"mysql_version"`
	Snapshot                 Snapshot        `json:"snapshot"`
	SQL                      SQLInput        `json:"sql_input"`
	Candidate                CandidateIndex  `json:"candidate_index"`
	ExplainJSON              json.RawMessage `json:"explain_json"`
}

// Run verifies the common environment, applies only the constrained candidate
// index, refreshes candidate statistics and retrieves an EXPLAIN JSON plan.
func (g Gate) Run(ctx context.Context, p Prepared) (Report, error) {
	if g.Baseline == nil || g.Candidate == nil {
		return Report{}, errors.New("baseline and candidate database handles are required")
	}
	version, err := sameServerVersion(ctx, g.Baseline, g.Candidate)
	if err != nil {
		return Report{}, err
	}
	gate, err := identicalSnapshots(ctx, g.Baseline, g.Candidate, g.Chunk)
	if err != nil {
		return Report{}, err
	}
	if err := requireSchema(ctx, g.Candidate, p.Spec); err != nil {
		return Report{}, err
	}
	created, err := ensureCandidateIndex(ctx, g.Candidate, p)
	if err != nil {
		return Report{}, err
	}
	if _, err := g.Candidate.ExecContext(ctx, "ANALYZE TABLE `orders`"); err != nil {
		return Report{}, fmt.Errorf("analyze candidate table: %w", err)
	}
	var explain string
	if err := g.Candidate.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+p.SQL).Scan(&explain); err != nil {
		return Report{}, fmt.Errorf("explain admitted SQL: %w", err)
	}
	if !json.Valid([]byte(explain)) {
		return Report{}, errors.New("MySQL returned invalid EXPLAIN JSON")
	}
	return Report{
		GeneratedAt:              time.Now().UTC().Format(time.RFC3339),
		EvidenceLevel:            "L1",
		PerformanceClaimEligible: false,
		MySQLVersion:             version,
		Snapshot:                 Snapshot{Digest: gate.Digest(), Rows: gate.Rows()},
		SQL:                      SQLInput{SQL: p.SQL, Signals: p.SQLSignals},
		Candidate:                CandidateIndex{Table: p.Spec.Table, Name: p.Spec.IndexName, DDL: p.DDL, Created: created},
		ExplainJSON:              json.RawMessage(explain),
	}, nil
}

// WriteJSON persists the one report object assembled by Run.
func WriteJSON(w interface{ Write([]byte) (int, error) }, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func sameServerVersion(ctx context.Context, baseline, candidateDB *sql.DB) (string, error) {
	var baselineVersion, candidateVersion string
	if err := baseline.QueryRowContext(ctx, "SELECT VERSION()").Scan(&baselineVersion); err != nil {
		return "", fmt.Errorf("query baseline MySQL version: %w", err)
	}
	if err := candidateDB.QueryRowContext(ctx, "SELECT VERSION()").Scan(&candidateVersion); err != nil {
		return "", fmt.Errorf("query candidate MySQL version: %w", err)
	}
	if baselineVersion != candidateVersion {
		return "", fmt.Errorf("%w: baseline=%s candidate=%s", measure.ErrServerVersionMismatch, baselineVersion, candidateVersion)
	}
	return baselineVersion, nil
}

func identicalSnapshots(ctx context.Context, baseline, candidateDB *sql.DB, chunk int) (measure.SnapshotGate, error) {
	b, err := snapshot.Compute(ctx, baseline, "baseline", chunk)
	if err != nil {
		return measure.SnapshotGate{}, fmt.Errorf("compute baseline snapshot: %w", err)
	}
	c, err := snapshot.Compute(ctx, candidateDB, "candidate", chunk)
	if err != nil {
		return measure.SnapshotGate{}, fmt.Errorf("compute candidate snapshot: %w", err)
	}
	if b.Rows != c.Rows || b.Digest != c.Digest {
		return measure.SnapshotGate{}, fmt.Errorf("%w: baseline rows=%d digest=%s candidate rows=%d digest=%s",
			measure.ErrSnapshotMismatch, b.Rows, b.Digest, c.Rows, c.Digest)
	}
	return measure.SnapshotGate{
		Baseline:  measure.SideSnapshot{Name: b.Name, Rows: b.Rows, Digest: b.Digest},
		Candidate: measure.SideSnapshot{Name: c.Name, Rows: c.Rows, Digest: c.Digest},
	}, nil
}

func requireSchema(ctx context.Context, db *sql.DB, spec candidate.Spec) error {
	var tableCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.TABLES
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, spec.Table).Scan(&tableCount); err != nil {
		return fmt.Errorf("check candidate table: %w", err)
	}
	if tableCount != 1 {
		return fmt.Errorf("%w: %s", ErrTableMissing, spec.Table)
	}
	for _, col := range spec.Columns {
		var columnCount int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`, spec.Table, col.Name).Scan(&columnCount); err != nil {
			return fmt.Errorf("check candidate column %q: %w", col.Name, err)
		}
		if columnCount != 1 {
			return fmt.Errorf("%w: %s.%s", ErrColumnMissing, spec.Table, col.Name)
		}
	}
	return nil
}

type indexColumn struct {
	Name      string
	Direction string
}

func ensureCandidateIndex(ctx context.Context, db *sql.DB, p Prepared) (bool, error) {
	actual, err := existingIndex(ctx, db, p.Spec.IndexName)
	if err != nil {
		return false, err
	}
	expected := expectedIndex(p.Spec)
	if len(actual) > 0 {
		if !sameIndex(actual, expected) {
			return false, fmt.Errorf("%w: %s", ErrIndexDefinitionConflict, p.Spec.IndexName)
		}
		return false, nil
	}
	if _, err := db.ExecContext(ctx, p.DDL); err != nil {
		return false, fmt.Errorf("create candidate index %q: %w", p.Spec.IndexName, err)
	}
	return true, nil
}

func existingIndex(ctx context.Context, db *sql.DB, name string) ([]indexColumn, error) {
	rows, err := db.QueryContext(ctx, `SELECT COLUMN_NAME, COLLATION FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'orders' AND INDEX_NAME = ? ORDER BY SEQ_IN_INDEX`, name)
	if err != nil {
		return nil, fmt.Errorf("inspect candidate index %q: %w", name, err)
	}
	defer rows.Close()
	var out []indexColumn
	for rows.Next() {
		var name string
		var collation sql.NullString
		if err := rows.Scan(&name, &collation); err != nil {
			return nil, err
		}
		direction := "ASC"
		if collation.Valid && collation.String == "D" {
			direction = "DESC"
		}
		out = append(out, indexColumn{Name: name, Direction: direction})
	}
	return out, rows.Err()
}

func expectedIndex(spec candidate.Spec) []indexColumn {
	out := make([]indexColumn, len(spec.Columns))
	for i, col := range spec.Columns {
		direction := strings.ToUpper(col.Direction)
		if direction == "" {
			direction = "ASC"
		}
		out[i] = indexColumn{Name: col.Name, Direction: direction}
	}
	return out
}

func sameIndex(actual, expected []indexColumn) bool {
	if len(actual) != len(expected) {
		return false
	}
	for i := range actual {
		if actual[i] != expected[i] {
			return false
		}
	}
	return true
}

// hasBindPlaceholder only treats a question mark in executable SQL as a bind
// marker. A question mark in a literal, quoted identifier or comment is data.
func hasBindPlaceholder(s string) bool {
	state := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		next := byte(0)
		if i+1 < len(s) {
			next = s[i+1]
		}
		switch state {
		case '\'':
			if c == '\\' && i+1 < len(s) {
				i++
			} else if c == '\'' && next == '\'' {
				i++
			} else if c == '\'' {
				state = 0
			}
			continue
		case '"':
			if c == '\\' && i+1 < len(s) {
				i++
			} else if c == '"' && next == '"' {
				i++
			} else if c == '"' {
				state = 0
			}
			continue
		case '`':
			if c == '`' && next == '`' {
				i++
			} else if c == '`' {
				state = 0
			}
			continue
		case '-':
			if c == '\n' {
				state = 0
			}
			continue
		case '/':
			if c == '*' && next == '/' {
				i++
				state = 0
			}
			continue
		}
		if c == '-' && next == '-' && i+2 < len(s) && (s[i+2] == ' ' || s[i+2] == '\t' || s[i+2] == '\r' || s[i+2] == '\n') {
			i++
			state = '-'
			continue
		}
		if c == '#' {
			state = '-'
			continue
		}
		if c == '/' && next == '*' {
			i++
			state = '/'
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			state = c
			continue
		}
		if c == '?' {
			return true
		}
	}
	return false
}
