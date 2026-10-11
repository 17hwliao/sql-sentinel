package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"sqlsentinel/internal/benchmark"
	workflow "sqlsentinel/internal/kafka"
	"sqlsentinel/internal/resultset"
	"sqlsentinel/internal/rewrite"
	"sqlsentinel/internal/shadowdata"
	"sqlsentinel/internal/sqladmit"
	"sqlsentinel/internal/verifierrpc"
	"time"
)

type rewriteReport struct {
	Protocol      string          `json:"protocol"`
	Equivalent    bool            `json:"result_equivalent"`
	Snapshot      string          `json:"snapshot_sha256"`
	OriginalSHA   string          `json:"original_sql_sha256"`
	CandidateSHA  string          `json:"candidate_sql_sha256"`
	Ordered       bool            `json:"ordered_result"`
	BaselinePlan  json.RawMessage `json:"baseline_plan"`
	CandidatePlan json.RawMessage `json:"candidate_plan"`
	Limitations   []string        `json:"limitations"`
}

func (r Runner) runRewrite(ctx context.Context, job workflow.Job, tables []string) (err error) {
	spec, e := rewrite.Decode(job.CandidateSpec, string(job.SQL))
	if e != nil {
		return workflow.ErrRejected
	}
	base, e := r.Baseline.Conn(ctx)
	if e != nil {
		return e
	}
	defer base.Close()
	cand, e := r.Candidate.Conn(ctx)
	if e != nil {
		return e
	}
	defer cand.Close()
	for _, conn := range []*sql.Conn{base, cand} {
		if _, e = conn.ExecContext(ctx, "SET SESSION max_execution_time=2000"); e != nil {
			return e
		}
	}
	var lock sql.NullInt64
	if cand.QueryRowContext(ctx, "SELECT GET_LOCK(CONCAT('sentinel-shadow:',DATABASE()),10)").Scan(&lock) != nil || !lock.Valid || lock.Int64 != 1 {
		return errors.New("shadow_busy")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		if e := r.cleanIndexes(cleanup, cand); e != nil {
			err = errors.New("shadow_cleanup_failed")
		}
		var released sql.NullInt64
		if cand.QueryRowContext(cleanup, "SELECT RELEASE_LOCK(CONCAT('sentinel-shadow:',DATABASE()))").Scan(&released) != nil || !released.Valid || released.Int64 != 1 {
			_ = cand.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.New("shadow_unlock_failed")
		}
	}()
	if e = r.cleanIndexes(ctx, cand); e != nil {
		return e
	}
	var bv, cv string
	if base.QueryRowContext(ctx, "SELECT VERSION()").Scan(&bv) != nil || cand.QueryRowContext(ctx, "SELECT VERSION()").Scan(&cv) != nil || bv != cv {
		return errors.New("shadow_versions_differ")
	}
	before, e := shadowdata.Compute(ctx, base, tables)
	if e != nil {
		return e
	}
	candidateBefore, e := shadowdata.Compute(ctx, cand, tables)
	if e != nil || before.Digest != candidateBefore.Digest {
		return errors.New("shadow_snapshots_differ")
	}
	verifierrpc.Notify(ctx, "rewrite_counterexample_check")
	evidence := rewriteReport{Protocol: "snapshot-rewrite.v1", Equivalent: true, Snapshot: before.Digest, OriginalSHA: job.SQLSHA256, CandidateSHA: benchmark.Digest([]byte(spec.SQL)), Ordered: sqladmit.OrderSensitive(string(job.SQL)), Limitations: []string{"Equivalence is checked on both current shadow snapshots; it is not a universal proof.", "No production SQL, DML or DDL is executed. Ordered results retain order; unordered duplicates and NULLs retain multiplicity."}}
	// Compare both queries on BOTH engines, so a coincidental cross-shadow
	// match cannot conceal a changed snapshot or a query counterexample.
	var reference string
	for _, db := range []*sql.Conn{base, cand} {
		for _, q := range []string{string(job.SQL), spec.SQL} {
			digest, _, e := resultset.Read(ctx, db, q, evidence.Ordered)
			if e != nil {
				return e
			}
			if reference == "" {
				reference = digest
			} else if digest != reference {
				evidence.Equivalent = false
			}
		}
	}
	var bp, cp string
	if base.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+string(job.SQL)).Scan(&bp) != nil || cand.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+spec.SQL).Scan(&cp) != nil {
		return errors.New("rewrite_explain_failed")
	}
	if !json.Valid([]byte(bp)) || !json.Valid([]byte(cp)) {
		return workflow.ErrRejected
	}
	evidence.BaselinePlan = json.RawMessage(bp)
	evidence.CandidatePlan = json.RawMessage(cp)
	bundle := map[string]any{"job_id": job.ID, "sql_sha256": job.SQLSHA256, "candidate_spec": spec, "rewrite": evidence, "versions": map[string]string{"schema": "backend.v3", "rules": "mysql-ast.v2", "rewrite": "snapshot-rewrite.v1", "benchmark": benchmark.ProtocolVersion}}
	if evidence.Equivalent && job.Options.Benchmark {
		p, e := benchmark.RunQueries(ctx, base, cand, string(job.SQL), spec.SQL, tables, before.Digest, r.Representative)
		if e != nil {
			return e
		}
		bundle["performance"] = p
	}
	after, e := shadowdata.Compute(ctx, base, tables)
	if e != nil {
		return e
	}
	candidateAfter, e := shadowdata.Compute(ctx, cand, tables)
	if e != nil {
		return e
	}
	if before.Digest != after.Digest || before.Digest != candidateAfter.Digest {
		return errors.New("rewrite_snapshot_changed")
	}
	raw, e := json.Marshal(bundle)
	if e != nil || len(raw) > 2<<20 {
		return errors.New("rewrite_report_limit")
	}
	verifierrpc.Notify(ctx, "report_persistence")
	result, e := r.Control.ExecContext(ctx, `INSERT INTO sql_sentinel_reports(job_id,report) SELECT job_id,? FROM sql_sentinel_jobs WHERE job_id=? AND status='RUNNING' AND lease_owner=? AND lease_until>UTC_TIMESTAMP(6) ON DUPLICATE KEY UPDATE report=VALUES(report),created_at=UTC_TIMESTAMP(6)`, raw, job.ID, job.LeaseOwner)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return workflow.ErrLeaseLost
	}
	if !evidence.Equivalent {
		return workflow.ErrRejected
	}
	return nil
}
