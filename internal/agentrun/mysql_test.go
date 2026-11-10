package agentrun

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sqlsentinel/internal/identity"
)

type deadlineModel struct {
	deadline time.Time
	calls    int
	observed bool
}

func (m *deadlineModel) DraftWithSystem(ctx context.Context, _, _ string) (string, error) {
	m.calls++
	m.deadline, _ = ctx.Deadline()
	<-ctx.Done()
	m.observed = true
	return "", ctx.Err()
}

func TestAgentRunMySQLIntegration(t *testing.T) {
	file := os.Getenv("SQL_SENTINEL_AGENT_CONTROL_DSN_FILE")
	if file == "" {
		t.Skip("dedicated disposable MySQL DSN file not configured")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("test DSN unavailable")
	}
	db, err := sql.Open("mysql", strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal("test database configuration invalid")
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		t.Fatal("test database unavailable")
	}
	var database string
	if err = db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil || database != "sentinel_improvement" {
		t.Fatal("isolated sentinel_improvement test database required")
	}
	newFixture := func(t *testing.T, outputs ...string) (*Service, *scriptedModel, *fakeTasks, identity.Principal, *atomic.Bool) {
		t.Helper()
		model := &scriptedModel{output: outputs}
		tasks := &fakeTasks{}
		id, e := newID()
		if e != nil {
			t.Fatal(e)
		}
		p := identity.Principal{Subject: "run_" + id[:12], Role: "writer", Tenant: "outer_test", Project: "project", Tables: []string{"orders"}, SessionID: "session_" + id}
		revoked := &atomic.Bool{}
		check := func(_ context.Context, actual identity.Principal, query string) error {
			if revoked.Load() || actual.SessionID == "" || actual.Tenant != p.Tenant || actual.Project != p.Project || !strings.Contains(query, "orders") {
				return ErrForbidden
			}
			return nil
		}
		svc, e := New(db, Config{Version: "outer.v1", DefaultModel: "fixture", ModelAliases: []string{"fixture"}, ToolsEnabled: true}, map[string]Model{"fixture": model}, tasks, check)
		if e != nil {
			t.Fatal(e)
		}
		if e = svc.EnsureSchema(ctx); e != nil {
			t.Fatal(e)
		}
		if e = svc.CheckSchema(ctx); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			_, _ = db.ExecContext(context.Background(), "DELETE FROM sql_sentinel_agent_runs WHERE tenant_id=? AND actor=?", p.Tenant, p.Subject)
		})
		return svc, model, tasks, p, revoked
	}
	create := func(t *testing.T, s *Service, p identity.Principal) Run {
		t.Helper()
		r, duplicate, e := s.Create(ctx, p, Input{RequestID: "stable_request", SQL: "SELECT id FROM orders WHERE user_id=42 LIMIT 10"})
		if e != nil || duplicate {
			t.Fatalf("create: duplicate=%v err=%v", duplicate, e)
		}
		return r
	}
	step := func(t *testing.T, s *Service) {
		t.Helper()
		if e := s.Step(ctx); e != nil {
			t.Fatal(e)
		}
	}
	read := func(t *testing.T, s *Service, p identity.Principal, r Run) Run {
		t.Helper()
		r, e := s.Get(ctx, p, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}

	t.Run("stable_request_and_current_scope", func(t *testing.T) {
		svc, _, _, p, _ := newFixture(t, `{"action":"finish"}`)
		r := create(t, svc, p)
		same, duplicate, e := svc.Create(ctx, p, Input{RequestID: r.RequestID, SQL: r.SQL})
		if e != nil || !duplicate || same.ID != r.ID {
			t.Fatalf("duplicate %v %v", duplicate, e)
		}
		if _, _, e = svc.Create(ctx, p, Input{RequestID: r.RequestID, SQL: "SELECT id FROM orders LIMIT 11"}); !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
		recovered, e := svc.GetByRequest(ctx, p, r.RequestID)
		if e != nil || recovered.ID != r.ID {
			t.Fatal("response loss recovery", e)
		}
		other := p
		other.Subject = "other"
		if _, e = svc.GetByRequest(ctx, other, r.RequestID); !errors.Is(e, ErrNotFound) {
			t.Fatal("scope leak", e)
		}
		fresh := p
		fresh.SessionID = "fresh_session"
		if _, e = svc.Get(ctx, fresh, r.ID); e != nil {
			t.Fatal("renewed identity cannot read", e)
		}
		if runs, e := svc.List(ctx, p); e != nil || len(runs) != 1 {
			t.Fatalf("list %d %v", len(runs), e)
		}
	})
	t.Run("real_storage_closed_loop", func(t *testing.T) {
		svc, model, tasks, p, _ := newFixture(t, `{"action":"submit"}`, `{"action":"finish"}`)
		r := create(t, svc, p)
		step(t, svc)
		r = read(t, svc, p, r)
		if r.Status != "waiting_tool" || r.Job == nil || r.ModelCalls != 1 {
			t.Fatalf("%+v", r)
		}
		step(t, svc)
		if model.calls != 1 {
			t.Fatal("model polled for tool")
		}
		tasks.mu.Lock()
		j := tasks.jobs[delivery(r)]
		j.Status = "COMPLETED"
		j.Evidence = &Evidence{Level: "L1", Outcome: "recommended", Summary: "已验证计划候选。", Recommended: true}
		tasks.jobs[delivery(r)] = j
		tasks.mu.Unlock()
		step(t, svc)
		step(t, svc)
		r = read(t, svc, p, r)
		if r.Status != "completed" || r.ModelCalls != 2 || r.ModelSummary || r.CostCNY != nil || r.UsageKnown || !strings.Contains(r.Answer, "非实测") {
			t.Fatalf("%+v", r)
		}
		if tasks.submitCalls != 1 {
			t.Fatal("multiple child jobs")
		}
	})
	t.Run("submit_response_lost_and_restart", func(t *testing.T) {
		svc, model, tasks, p, _ := newFixture(t, `{"action":"submit"}`)
		tasks.unknown = true
		r := create(t, svc, p)
		step(t, svc)
		r = read(t, svc, p, r)
		if r.Job != nil || r.Status != "waiting_tool" {
			t.Fatalf("%+v", r)
		}
		// A new service instance recovers the same durable association.
		replacement, e := New(db, svc.config, svc.models, tasks, svc.check)
		if e != nil {
			t.Fatal(e)
		}
		step(t, replacement)
		r = read(t, svc, p, r)
		if r.Job == nil || r.Job.DeliveryID != delivery(r) || tasks.submitCalls != 1 || model.calls != 1 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("waiting_run_does_not_starve_new_run", func(t *testing.T) {
		svc, model, tasks, p, _ := newFixture(t, `{"action":"submit"}`)
		first := create(t, svc, p)
		second, _, e := svc.Create(ctx, p, Input{RequestID: "second_waiter", SQL: first.SQL})
		if e != nil {
			t.Fatal(e)
		}
		step(t, svc)
		step(t, svc)
		first = read(t, svc, p, first)
		second = read(t, svc, p, second)
		if first.Job == nil || second.Job == nil || model.calls != 2 || tasks.submitCalls != 2 {
			t.Fatalf("waiting run starved newer request: first=%+v second=%+v", first, second)
		}
	})
	t.Run("model_inflight_crash_no_replay", func(t *testing.T) {
		svc, model, _, p, _ := newFixture(t, `{"action":"submit"}`)
		r := create(t, svc, p)
		claimed, e := svc.claim(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if e = svc.reserve(ctx, &claimed); e != nil {
			t.Fatal(e)
		}
		if _, e = db.ExecContext(ctx, "UPDATE sql_sentinel_agent_runs SET lease_until=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND) WHERE run_id=?", r.ID); e != nil {
			t.Fatal(e)
		}
		step(t, svc)
		r = read(t, svc, p, r)
		if r.Status != "outcome_unknown" || r.StopReason != "model_dispatch_unknown" || r.ModelCalls != 1 || model.calls != 0 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("cross_attempt_budget_and_fence", func(t *testing.T) {
		svc, _, _, p, _ := newFixture(t, `{"action":"finish"}`)
		r := create(t, svc, p)
		claimed, e := svc.claim(ctx)
		if e != nil {
			t.Fatal(e)
		}
		for i := 0; i < MaxModelCalls; i++ {
			if e = svc.reserve(ctx, &claimed); e != nil {
				t.Fatal(e)
			}
		}
		if e = svc.reserve(ctx, &claimed); !errors.Is(e, ErrBudget) {
			t.Fatal("budget", e)
		}
		if _, e = db.ExecContext(ctx, "UPDATE sql_sentinel_agent_runs SET lease_until=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND) WHERE run_id=?", r.ID); e != nil {
			t.Fatal(e)
		}
		replacement, e := svc.claim(ctx)
		if e != nil {
			t.Fatal(e)
		}
		claimed.Answer = "stale writer"
		if e = svc.save(ctx, &claimed, true); !errors.Is(e, ErrLeaseLost) {
			t.Fatal("old writer", e)
		}
		if replacement.ModelCalls != 4 {
			t.Fatal("budget reset")
		}
		if e = svc.reserve(ctx, &replacement); !errors.Is(e, ErrBudget) {
			t.Fatal(e)
		}
	})
	t.Run("queued_cancel_no_model_or_child", func(t *testing.T) {
		svc, model, tasks, p, _ := newFixture(t, `{"action":"submit"}`)
		r := create(t, svc, p)
		r, e := svc.Cancel(ctx, p, r.ID)
		if e != nil || r.Status != "cancelled" {
			t.Fatal(e)
		}
		step(t, svc)
		r = read(t, svc, p, r)
		if r.Status != "cancelled" || r.pendingCancel || model.calls != 0 || tasks.submitCalls != 0 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("cancel_running_child", func(t *testing.T) {
		svc, _, tasks, p, _ := newFixture(t, `{"action":"submit"}`)
		r := create(t, svc, p)
		step(t, svc)
		if _, e := svc.Cancel(ctx, p, r.ID); e != nil {
			t.Fatal(e)
		}
		step(t, svc)
		r = read(t, svc, p, r)
		if r.Status != "cancelled" || r.pendingCancel || r.Job == nil || r.Job.Status != "CANCELLED" || tasks.cancels != 1 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("cancel_after_unknown_submit_recovers_same_job", func(t *testing.T) {
		svc, _, tasks, p, _ := newFixture(t, `{"action":"submit"}`)
		tasks.unknown = true
		r := create(t, svc, p)
		step(t, svc)
		if _, e := svc.Cancel(ctx, p, r.ID); e != nil {
			t.Fatal(e)
		}
		step(t, svc)
		r = read(t, svc, p, r)
		if r.Status != "cancelled" || r.Job == nil || tasks.submitCalls != 1 || tasks.cancels != 1 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("authorization_revoked_before_dispatch", func(t *testing.T) {
		svc, model, tasks, p, revoked := newFixture(t, `{"action":"submit"}`)
		r := create(t, svc, p)
		revoked.Store(true)
		step(t, svc)
		stored, e := svc.load(ctx, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		if stored.StopReason != "authorization_failed" || stored.ModelCalls != 0 || model.calls != 0 || tasks.submitCalls != 0 {
			t.Fatalf("%+v", stored)
		}
		if _, e = svc.Get(ctx, p, r.ID); !errors.Is(e, ErrForbidden) {
			t.Fatal(e)
		}
	})
	t.Run("model_protocol_and_error", func(t *testing.T) {
		svc, model, tasks, p, _ := newFixture(t, `{"action":"submit","tenant_id":"other"}`)
		r := create(t, svc, p)
		step(t, svc)
		r = read(t, svc, p, r)
		if r.StopReason != "model_protocol_invalid" || r.ModelCalls != 1 || tasks.submitCalls != 0 {
			t.Fatalf("%+v", r)
		}
		model.err = context.DeadlineExceeded
		second, _, e := svc.Create(ctx, p, Input{RequestID: "second", SQL: r.SQL})
		if e != nil {
			t.Fatal(e)
		}
		step(t, svc)
		second = read(t, svc, p, second)
		if second.Status != "outcome_unknown" || second.ModelCalls != 1 {
			t.Fatalf("%+v", second)
		}
	})
	t.Run("identity_dependency_outage_is_not_revocation", func(t *testing.T) {
		svc, model, tasks, p, _ := newFixture(t, `{"action":"submit"}`)
		r := create(t, svc, p)
		svc.check = func(context.Context, identity.Principal, string) error { return ErrUnavailable }
		if _, _, e := svc.Create(ctx, p, Input{RequestID: "dependency_down", SQL: r.SQL}); !errors.Is(e, ErrUnavailable) {
			t.Fatal("create misclassified outage", e)
		}
		if _, e := svc.Get(ctx, p, r.ID); !errors.Is(e, ErrUnavailable) {
			t.Fatal("get misclassified outage", e)
		}
		step(t, svc)
		stored, e := svc.load(ctx, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		if stored.Status != "failed" || stored.StopReason != "infrastructure_failed" || stored.ModelCalls != 0 || model.calls != 0 || tasks.submitCalls != 0 {
			t.Fatalf("%+v", stored)
		}
	})
	t.Run("oversized_context_zero_dispatch", func(t *testing.T) {
		svc, model, _, p, _ := newFixture(t, `{"action":"submit"}`)
		query := "SELECT id FROM orders WHERE user_id=1 /*" + strings.Repeat("a", 40<<10) + "*/ LIMIT 1"
		r, _, e := svc.Create(ctx, p, Input{RequestID: "large", SQL: query})
		if e != nil {
			t.Fatal(e)
		}
		step(t, svc)
		r = read(t, svc, p, r)
		if r.StopReason != "context_budget_exhausted" || r.ModelCalls != 0 || model.calls != 0 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("run_deadline_bounds_inflight_model", func(t *testing.T) {
		svc, _, tasks, p, _ := newFixture(t, `{"action":"submit"}`)
		r := create(t, svc, p)
		blocking := &deadlineModel{}
		svc.models["fixture"] = blocking
		deadline := time.Now().UTC().Add(500 * time.Millisecond).Truncate(time.Microsecond)
		if _, e := db.ExecContext(ctx, "UPDATE sql_sentinel_agent_runs SET deadline=? WHERE run_id=?", deadline, r.ID); e != nil {
			t.Fatal(e)
		}
		started := time.Now()
		step(t, svc)
		elapsed := time.Since(started)
		r = read(t, svc, p, r)
		if !blocking.observed || blocking.calls != 1 || !blocking.deadline.Equal(deadline) || elapsed > 2*time.Second || r.Status != "failed" || r.StopReason != "deadline_exhausted" || r.ModelCalls != 1 || tasks.submitCalls != 0 {
			t.Fatalf("deadline was not enforced: elapsed=%s model_deadline=%s expected=%s run=%+v", elapsed, blocking.deadline, deadline, r)
		}
	})
	t.Run("cancel_submit_overlap_never_leaks_child", func(t *testing.T) {
		svc, model, tasks, p, _ := newFixture(t, `{"action":"submit"}`)
		tasks.entered = make(chan struct{})
		tasks.release = make(chan struct{})
		r := create(t, svc, p)
		done := make(chan error, 1)
		go func() { done <- svc.Step(ctx) }()
		select {
		case <-tasks.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("submit not entered")
		}
		if _, e := svc.Cancel(ctx, p, r.ID); e != nil {
			t.Fatal(e)
		}
		close(tasks.release)
		if e := <-done; e != nil {
			t.Fatal(e)
		}
		r = read(t, svc, p, r)
		if r.Status != "cancelled" || r.pendingCancel || r.Job == nil || r.Job.Status != "CANCELLED" || model.calls != 1 || tasks.submitCalls != 1 || tasks.cancels != 1 {
			t.Fatalf("%+v", r)
		}
	})
}
