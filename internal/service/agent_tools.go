package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"sqlsentinel/internal/agentgraph"
	"sqlsentinel/internal/benchmark"
	"sqlsentinel/internal/candidate"
	"sqlsentinel/internal/historicalprior"
	workflow "sqlsentinel/internal/kafka"
	"sqlsentinel/internal/plancompare"
	"sqlsentinel/internal/shadowdata"
	"sqlsentinel/internal/shadowgate"
	"sqlsentinel/internal/sqladmit"
)

// agentTools adapts the existing isolated executor. It owns no connections,
// locks or DDL implementation; explain returns only after its deferred cleanup.
// The function fields are narrow test seams, not model-provided capabilities.
type agentTools struct {
	job               workflow.Job
	tables            []string
	allowed           map[string]bool
	check             func(context.Context) error
	reserve           func(context.Context, string) error
	describe          func(context.Context) ([]shadowdata.Table, error)
	describeCandidate func(context.Context) ([]shadowdata.Table, error)
	snapshot          func(context.Context, bool) (shadowdata.Snapshot, error)
	version           func(context.Context, bool) (string, error)
	baselinePlan      func(context.Context) ([]byte, error)
	explain           func(context.Context, string, []byte) (shadowgate.Report, []byte, error)
	history           func(context.Context, string) (historicalprior.Result, error)
	initial           *agentgraph.LoopInput
	sequence          int
}

var agentJobID = regexp.MustCompile(`^[a-f0-9]{24,64}$`)

func newAgentTools(r Runner, job workflow.Job, tables []string, check func(context.Context) error, reserve func(context.Context, string) error) *agentTools {
	t := &agentTools{job: job, tables: append([]string(nil), tables...), allowed: map[string]bool{}, check: check, reserve: reserve}
	for _, name := range tables {
		t.allowed[name] = r.AllowedTables == nil || r.AllowedTables[name]
	}
	t.describe = func(ctx context.Context) ([]shadowdata.Table, error) {
		return shadowdata.Describe(ctx, r.Baseline, t.tables)
	}
	t.describeCandidate = func(ctx context.Context) ([]shadowdata.Table, error) {
		return shadowdata.Describe(ctx, r.Candidate, t.tables)
	}
	t.snapshot = func(ctx context.Context, candidateSide bool) (shadowdata.Snapshot, error) {
		db := r.Baseline
		if candidateSide {
			db = r.Candidate
		}
		return shadowdata.Compute(ctx, db, t.tables)
	}
	t.version = func(ctx context.Context, candidateSide bool) (string, error) {
		db := r.Baseline
		if candidateSide {
			db = r.Candidate
		}
		var version string
		err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version)
		return version, err
	}
	t.baselinePlan = func(ctx context.Context) ([]byte, error) {
		var plan string
		err := r.Baseline.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+string(job.SQL)).Scan(&plan)
		return []byte(plan), err
	}
	t.explain = func(ctx context.Context, query string, raw []byte) (shadowgate.Report, []byte, error) {
		var performance *benchmark.Report
		return r.explain(ctx, query, raw, false, &performance)
	}
	if r.History != nil {
		t.history = func(ctx context.Context, table string) (historicalprior.Result, error) {
			return r.History.Search(ctx, job.Options.TenantID, job.Options.ProjectID, table, "", t.allowed)
		}
	}
	return t
}

func (t *agentTools) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.check == nil {
		return agentgraph.ErrAgentFence
	}
	return t.check(ctx)
}

func (t *agentTools) Reserve(ctx context.Context, kind string) error {
	if err := t.Check(ctx); err != nil {
		return err
	}
	if (kind != "model" && kind != "evaluation") || t.reserve == nil {
		return agentgraph.ErrAgentBudget
	}
	return t.reserve(ctx, kind)
}

// Initial binds current evidence to the trusted task before the first decision.
// Every later evaluation checks the same snapshot/schema/version again.
func (t *agentTools) Initial(ctx context.Context) (agentgraph.LoopInput, error) {
	if err := t.Check(ctx); err != nil {
		return agentgraph.LoopInput{}, err
	}
	actual, err := sqladmit.Inspect(string(t.job.SQL))
	if err != nil || !sqladmit.Admit(string(t.job.SQL)).Accepted || !agentJobID.MatchString(t.job.ID) || len(t.tables) == 0 || len(t.tables) > 16 || agentDigest(t.job.SQL) != t.job.SQLSHA256 {
		return agentgraph.LoopInput{}, agentgraph.ErrAgentAuthorization
	}
	if !sameAgentTables(actual, t.tables) {
		return agentgraph.LoopInput{}, agentgraph.ErrAgentAuthorization
	}
	for _, name := range t.tables {
		if !t.allowed[name] {
			return agentgraph.LoopInput{}, agentgraph.ErrAgentAuthorization
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	schema, err := t.describe(ctx)
	if err != nil {
		return agentgraph.LoopInput{}, t.failure(ctx, err, "schema_evidence_failed")
	}
	if !sameAgentTables(schemaNames(schema), t.tables) {
		return agentgraph.LoopInput{}, agentgraph.ErrAgentAuthorization
	}
	schemaSHA, err := agentSchemaHash(schema)
	if err != nil {
		return agentgraph.LoopInput{}, t.failure(ctx, err, "schema_evidence_failed")
	}
	if err = t.requireCandidateSchema(ctx, schemaSHA); err != nil {
		return agentgraph.LoopInput{}, err
	}
	snapshot, err := t.snapshot(ctx, false)
	if err != nil {
		return agentgraph.LoopInput{}, t.failure(ctx, err, "snapshot_evidence_failed")
	}
	candidateSnapshot, err := t.snapshot(ctx, true)
	if err != nil || candidateSnapshot.Digest != snapshot.Digest {
		return agentgraph.LoopInput{}, t.failure(ctx, err, "snapshot_changed")
	}
	version, err := t.version(ctx, false)
	if err != nil {
		return agentgraph.LoopInput{}, t.failure(ctx, err, "version_evidence_failed")
	}
	candidateVersion, err := t.version(ctx, true)
	if err != nil || version == "" || version != candidateVersion {
		return agentgraph.LoopInput{}, t.failure(ctx, err, "server_version_changed")
	}
	plan, err := t.baselinePlan(ctx)
	if err != nil || !json.Valid(plan) {
		return agentgraph.LoopInput{}, t.failure(ctx, err, "baseline_evidence_failed")
	}
	if len(plan) > 12<<10 {
		return agentgraph.LoopInput{}, agentgraph.ErrAgentBudget
	}
	// Retain the actual plan for the decision input. Reports contain only the
	// parsed comparison summaries, never reflected database/provider error text.
	if _, err = plancompare.Parse(plan); err != nil {
		return agentgraph.LoopInput{}, t.failure(ctx, err, "baseline_evidence_failed")
	}
	input := agentgraph.LoopInput{JobID: t.job.ID, Attempt: t.job.Attempts, SQL: string(t.job.SQL), SQLSHA256: t.job.SQLSHA256, Tables: append([]string(nil), t.tables...), Schema: schema, BaselinePlan: append(json.RawMessage(nil), plan...), SnapshotSHA256: snapshot.Digest, SchemaSHA256: schemaSHA, MySQLVersion: version, AllowHistory: t.history != nil}
	if err = t.Check(ctx); err != nil {
		return agentgraph.LoopInput{}, err
	}
	t.initial = &input
	return input, nil
}

func (t *agentTools) Inspect(ctx context.Context, table string) (agentgraph.Observation, error) {
	if err := t.Check(ctx); err != nil {
		return agentgraph.Observation{}, err
	}
	if t.initial == nil {
		return agentgraph.Observation{}, agentgraph.ErrAgentInfrastructure
	}
	if !t.allowed[table] {
		return agentgraph.Observation{}, agentgraph.ErrAgentAuthorization
	}
	for _, schema := range t.initial.Schema {
		if schema.Name == table {
			facts, err := json.Marshal(schema)
			if err != nil {
				return agentgraph.Observation{}, t.failure(ctx, err, "schema_context_limit")
			}
			if len(facts) > 12<<10 {
				return agentgraph.Observation{}, agentgraph.ErrAgentBudget
			}
			return agentgraph.Observation{Action: "inspect_schema", Status: "ok", Facts: facts, SnapshotSHA256: t.initial.SnapshotSHA256, SchemaSHA256: t.initial.SchemaSHA256, MySQLVersion: t.initial.MySQLVersion, EvidenceLevel: "L1"}, nil
		}
	}
	return agentgraph.Observation{}, agentgraph.ErrAgentAuthorization
}

func (t *agentTools) Search(ctx context.Context, table string) (agentgraph.Observation, error) {
	if err := t.Check(ctx); err != nil {
		return agentgraph.Observation{}, err
	}
	if !t.allowed[table] {
		return agentgraph.Observation{}, agentgraph.ErrAgentAuthorization
	}
	observation := agentgraph.Observation{Action: "search_history", Status: "unavailable", Code: "history_not_configured"}
	if t.history == nil {
		return observation, nil
	}
	step, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	prior, err := t.history(step, table)
	if checkErr := t.Check(ctx); checkErr != nil {
		return agentgraph.Observation{}, checkErr
	}
	if errors.Is(err, historicalprior.ErrUnauthorized) {
		return agentgraph.Observation{}, agentgraph.ErrAgentAuthorization
	}
	if err != nil {
		if errors.Is(err, historicalprior.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) {
			observation.Code = "history_unavailable"
			return observation, nil
		}
		return agentgraph.Observation{}, t.failure(ctx, err, "history_failed")
	}
	type hint struct {
		Table          string   `json:"table"`
		Columns        []string `json:"columns"`
		HistoricalOnly bool     `json:"historical_only"`
	}
	hints := []hint{}
	for _, d := range prior.Documents {
		if d.Tenant != t.job.Options.TenantID || d.Project != t.job.Options.ProjectID || d.Table != table || !d.Valid(time.Now()) {
			return agentgraph.Observation{}, agentgraph.ErrAgentAuthorization
		}
		for _, name := range d.Tables {
			if !t.allowed[name] {
				return agentgraph.Observation{}, agentgraph.ErrAgentAuthorization
			}
		}
		hints = append(hints, hint{Table: d.Table, Columns: append([]string(nil), d.Columns...), HistoricalOnly: true})
		if len(hints) == 3 {
			break
		}
	}
	facts, _ := json.Marshal(struct {
		HistoricalOnly bool   `json:"historical_only"`
		Hints          []hint `json:"hints"`
	}{true, hints})
	if len(facts) > 4096 {
		return agentgraph.Observation{}, agentgraph.ErrAgentBudget
	}
	observation.Status = "ok"
	observation.Code = ""
	observation.Facts = facts
	return observation, nil
}

func (t *agentTools) Evaluate(ctx context.Context, spec candidate.Spec) (agentgraph.Observation, error) {
	if err := t.Check(ctx); err != nil {
		return agentgraph.Observation{}, err
	}
	if t.initial == nil {
		return agentgraph.Observation{}, agentgraph.ErrAgentInfrastructure
	}
	if !t.allowed[spec.Table] {
		return agentgraph.Observation{}, agentgraph.ErrAgentAuthorization
	}
	if _, err := shadowgate.Prepare(string(t.job.SQL), spec); err != nil {
		return agentRejected("candidate_admission_rejected"), nil
	}
	if !agentColumnsAllowed(t.initial.Schema, spec) {
		return agentRejected("candidate_column_not_allowed"), nil
	}
	step, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := t.unchanged(step, false); err != nil {
		return agentgraph.Observation{}, err
	}
	if err := t.Check(step); err != nil {
		return agentgraph.Observation{}, err
	}
	t.sequence++
	spec.IndexName = fmt.Sprintf("idx_cand_%s_%d", t.job.ID[:24], t.sequence)
	expectedDDL, err := candidate.CompileCreateIndex(spec)
	if err != nil {
		return agentgraph.Observation{}, t.failure(step, err, "candidate_binding_failed")
	}
	raw, _ := json.Marshal(spec)
	report, baseline, err := t.explain(step, string(t.job.SQL), raw)
	// explain's cleanup has completed before this call returns. Fence failure
	// takes precedence, and no following candidate runs after any unsafe error.
	if checkErr := t.Check(step); checkErr != nil {
		return agentgraph.Observation{}, checkErr
	}
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1071 {
			return agentRejected("candidate_key_too_long"), nil
		}
		return agentgraph.Observation{}, t.failure(step, err, "shadow_verification_failed")
	}
	if report.EvidenceLevel != "L1" || report.EligibleForPerformanceClaim || report.Snapshot.Digest != t.initial.SnapshotSHA256 || report.MySQLVersion != t.initial.MySQLVersion || report.Candidate.Table != spec.Table || report.Candidate.Name != spec.IndexName || report.Candidate.DDL != expectedDDL || report.SQL.SQL != string(t.job.SQL) {
		return agentgraph.Observation{}, t.failure(step, nil, "evidence_context_changed")
	}
	if err = t.unchanged(step, true); err != nil {
		return agentgraph.Observation{}, err
	}
	if !json.Valid(baseline) || !json.Valid(report.ExplainJSON) {
		return agentgraph.Observation{}, t.failure(step, nil, "plan_context_limit")
	}
	if len(baseline) > 12<<10 || len(report.ExplainJSON) > 12<<10 {
		return agentgraph.Observation{}, agentgraph.ErrAgentBudget
	}
	basePlan, e := plancompare.Parse(baseline)
	if e != nil {
		return agentgraph.Observation{}, t.failure(step, e, "baseline_evidence_failed")
	}
	newPlan, e := plancompare.Parse(report.ExplainJSON)
	if e != nil {
		return agentgraph.Observation{}, t.failure(step, e, "candidate_evidence_failed")
	}
	facts, e := json.Marshal(struct {
		Baseline         plancompare.Summary    `json:"baseline"`
		Candidate        plancompare.Summary    `json:"candidate"`
		Difference       plancompare.Difference `json:"difference"`
		Measured         bool                   `json:"measured"`
		CleanupConfirmed bool                   `json:"cleanup_confirmed"`
	}{basePlan.Summary, newPlan.Summary, plancompare.Diff(basePlan.Summary, newPlan.Summary), false, true})
	if e != nil {
		return agentgraph.Observation{}, t.failure(step, e, "comparison_context_limit")
	}
	if len(facts) > 12<<10 {
		return agentgraph.Observation{}, agentgraph.ErrAgentBudget
	}
	hash, e := agentgraph.CanonicalCandidateHash(spec)
	if e != nil {
		return agentgraph.Observation{}, t.failure(step, e, "candidate_binding_failed")
	}
	if err = t.Check(step); err != nil {
		return agentgraph.Observation{}, err
	}
	return agentgraph.Observation{Action: "evaluate_index", Status: "ok", Facts: facts, Candidate: &spec, CandidateSHA256: hash, SnapshotSHA256: report.Snapshot.Digest, SchemaSHA256: t.initial.SchemaSHA256, MySQLVersion: report.MySQLVersion, EvidenceLevel: "L1", Verified: true}, nil
}

func (t *agentTools) unchanged(ctx context.Context, includeCandidate bool) error {
	if err := t.Check(ctx); err != nil {
		return err
	}
	schema, err := t.describe(ctx)
	if err != nil {
		return t.failure(ctx, err, "schema_evidence_failed")
	}
	hash, err := agentSchemaHash(schema)
	if err != nil || hash != t.initial.SchemaSHA256 {
		// Initial evidence already fit. Becoming oversized now is a changed
		// context, not permission to reuse the earlier candidate's evidence.
		return t.failure(ctx, nil, "schema_changed")
	}
	if err = t.requireCandidateSchema(ctx, t.initial.SchemaSHA256); err != nil {
		return err
	}
	for _, side := range []bool{false, true} {
		if side && !includeCandidate {
			continue
		}
		snapshot, err := t.snapshot(ctx, side)
		if err != nil || snapshot.Digest != t.initial.SnapshotSHA256 {
			return t.failure(ctx, err, "snapshot_changed")
		}
		version, err := t.version(ctx, side)
		if err != nil || version != t.initial.MySQLVersion {
			return t.failure(ctx, err, "server_version_changed")
		}
	}
	return t.Check(ctx)
}

func (t *agentTools) requireCandidateSchema(ctx context.Context, expected string) error {
	if t.describeCandidate == nil {
		return t.failure(ctx, nil, "candidate_schema_unavailable")
	}
	schema, err := t.describeCandidate(ctx)
	if err != nil {
		return t.failure(ctx, err, "candidate_schema_failed")
	}
	hash, err := agentSchemaHash(schema)
	if err != nil || hash != expected {
		return t.failure(ctx, nil, "candidate_schema_changed")
	}
	return nil
}

func (t *agentTools) failure(ctx context.Context, cause error, code string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(cause, agentgraph.ErrAgentBudget) {
		return agentgraph.ErrAgentBudget
	}
	return fmt.Errorf("%w: %s", agentgraph.ErrAgentInfrastructure, code)
}
func agentRejected(code string) agentgraph.Observation {
	return agentgraph.Observation{Action: "evaluate_index", Status: "rejected", Code: code}
}
func agentDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func schemaNames(schema []shadowdata.Table) []string {
	names := make([]string, 0, len(schema))
	for _, table := range schema {
		names = append(names, table.Name)
	}
	return names
}
func sameAgentTables(left, right []string) bool {
	a, b := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(a)
	sort.Strings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func agentSchemaHash(schema []shadowdata.Table) (string, error) {
	copySchema := make([]shadowdata.Table, len(schema))
	for i, t := range schema {
		copySchema[i] = t
		copySchema[i].EstimatedRows = 0
		copySchema[i].Columns = append([]shadowdata.Column(nil), t.Columns...)
		copySchema[i].Indexes = nil
		for _, idx := range t.Indexes {
			if !strings.HasPrefix(idx.Name, "idx_cand_") {
				copySchema[i].Indexes = append(copySchema[i].Indexes, idx)
			}
		}
	}
	sort.Slice(copySchema, func(i, j int) bool { return copySchema[i].Name < copySchema[j].Name })
	raw, err := json.Marshal(copySchema)
	if err != nil {
		return "", err
	}
	if len(raw) > 12<<10 {
		return "", agentgraph.ErrAgentBudget
	}
	return agentDigest(raw), nil
}
func agentColumnsAllowed(schema []shadowdata.Table, spec candidate.Spec) bool {
	for _, table := range schema {
		if table.Name != spec.Table {
			continue
		}
		for _, col := range spec.Columns {
			found := false
			for _, allowed := range table.Columns {
				if allowed.Name == col.Name {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	return false
}
