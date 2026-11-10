package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"sqlsentinel/internal/agentgraph"
	"sqlsentinel/internal/agentrun"
	"sqlsentinel/internal/identity"
)

type outerDecisionFixture struct{ calls int }

func (m *outerDecisionFixture) DraftWithSystem(_ context.Context, rules, prompt string) (string, error) {
	m.calls++
	if rules != agentrun.Rules {
		return "", errors.New("policy_missing")
	}
	var p struct{ Job *agentrun.Job }
	if json.Unmarshal([]byte(prompt), &p) != nil {
		return "", errors.New("invalid_fixture_prompt")
	}
	if p.Job == nil {
		return `{"action":"submit"}`, nil
	}
	return `{"action":"finish"}`, nil
}

type lostSubmitFixture struct {
	RunTasks
	lose bool
}

func (t *lostSubmitFixture) Submit(ctx context.Context, p identity.Principal, id, q string) (agentrun.Job, error) {
	j, e := t.RunTasks.Submit(ctx, p, id, q)
	if e == nil && t.lose {
		t.lose = false
		return agentrun.Job{}, agentrun.ErrOutcomeUnknown
	}
	return j, e
}

func TestWebsiteAgentRealMySQLIntegration(t *testing.T) {
	f := openAgentIntegration(t)
	ctx := context.Background()
	backend := &Server{Store: f.store, Auth: f.auth, AllowedTables: map[string]bool{"orders": true}}
	model := &outerDecisionFixture{}
	tasks := &lostSubmitFixture{RunTasks: RunTasks{Server: backend}}
	runs, err := agentrun.New(f.control, agentrun.Config{Version: "web-fixture.v1", DefaultModel: "fixture", ModelAliases: []string{"fixture"}, ToolsEnabled: true}, map[string]agentrun.Model{"fixture": model}, tasks, backend.CheckAgentRunPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	if err = runs.EnsureSchema(ctx); err != nil {
		t.Fatal("outer schema unavailable")
	}
	backend.AgentRuns = runs
	f.server.Close()
	f.server = httptest.NewServer(backend.Handler())
	t.Cleanup(f.server.Close)
	t.Cleanup(func() { _, _ = f.control.Exec("DELETE FROM sql_sentinel_agent_runs WHERE tenant_id=?", f.tenant) })
	readRun := func(id string) agentrun.Run {
		t.Helper()
		status, raw, _ := f.request("GET", "/agent/runs/"+id, f.writer, nil)
		if status != 200 {
			t.Fatalf("run read failed %d %s", status, safeIntegrationCode(raw))
		}
		var r agentrun.Run
		if json.Unmarshal(raw, &r) != nil {
			t.Fatal("invalid run response")
		}
		return r
	}
	submit := func(request string) agentrun.Run {
		t.Helper()
		status, raw, _ := f.request("POST", "/agent/runs", f.writer, map[string]any{"request_id": request, "sql": "SELECT id FROM orders WHERE user_id=42 AND payload='needle'", "message": "Diagnose this SQL; do not change production."})
		if status != 202 {
			t.Fatalf("run creation failed %d %s", status, safeIntegrationCode(raw))
		}
		var r agentrun.Run
		if json.Unmarshal(raw, &r) != nil {
			t.Fatal("invalid created run")
		}
		return r
	}
	step := func() {
		t.Helper()
		if err := runs.Step(ctx); err != nil {
			t.Fatalf("outer step failed %s", agentrun.ErrorCode(err))
		}
	}

	t.Run("http_outer_to_actual_inner_mysql_report", func(t *testing.T) {
		f.resetOrders()
		r := submit(f.tenant + "_web_success")
		duplicate := submit(r.RequestID)
		if duplicate.ID != r.ID {
			t.Fatal("idempotent run created twice")
		}
		status, _, _ := f.request("POST", "/agent/runs", f.writer, map[string]any{"request_id": r.RequestID, "sql": "SELECT id FROM orders WHERE id=7"})
		if status != 409 {
			t.Fatal("changed input accepted under old request ID")
		}
		step()
		r = readRun(r.ID)
		if r.Job == nil || r.Job.Status != "QUEUED" {
			t.Fatal("website did not create durable diagnostic job")
		}
		job, e := f.store.Get(ctx, r.Job.DeliveryID)
		if e != nil {
			t.Fatal("durable job missing")
		}
		inner, count := compatibleFixtureNode(t, true)
		f.run(job, inner)
		f.assertClean()
		step()
		step()
		r = readRun(r.ID)
		if r.Status != "completed" || r.Job == nil || r.Job.Evidence == nil || !r.Job.Evidence.Recommended || r.Job.Evidence.Level != "L1" || r.ModelCalls != 2 || r.ModelSummary || r.CostCNY != nil || r.UsageKnown || count.Load() != 3 {
			t.Fatalf("closed loop not proven status=%s outer=%d inner=%d", r.Status, r.ModelCalls, count.Load())
		}
		if !strings.Contains(r.Answer, "非实测性能收益") {
			t.Fatal("outer answer lost evidence limitation")
		}
		foreign, _ := f.issue("other", "writer", f.tenant, f.project, []string{"orders"})
		status, _, _ = f.request("GET", "/agent/runs/"+r.ID, foreign, nil)
		if status != 404 {
			t.Fatal("run leaked to another subject")
		}
	})
	t.Run("lost_submit_response_recovers_one_real_job", func(t *testing.T) {
		f.resetOrders()
		tasks.lose = true
		r := submit(f.tenant + "_web_lost")
		step()
		r = readRun(r.ID)
		if r.Job != nil {
			t.Fatal("fixture did not discard committed submit response")
		}
		step()
		r = readRun(r.ID)
		if r.Job == nil {
			t.Fatal("original durable job not recovered")
		}
		var n int
		if f.control.QueryRow("SELECT COUNT(*) FROM sql_sentinel_jobs WHERE delivery_id=?", r.Job.DeliveryID).Scan(&n) != nil || n != 1 {
			t.Fatal("lost response duplicated job")
		}
		job, e := f.store.Get(ctx, r.Job.DeliveryID)
		if e != nil {
			t.Fatal(e)
		}
		f.run(job, integrationFinishNode())
		step()
		step()
		r = readRun(r.ID)
		if r.Status != "completed" || r.Job.Evidence == nil || r.Job.Evidence.Outcome != "no_change" || r.Job.Evidence.Recommended {
			t.Fatal("legitimate no-candidate result became failure or fabricated recommendation")
		}
	})
	t.Run("explicit_cancel_and_missing_sql", func(t *testing.T) {
		status, _, _ := f.request("POST", "/agent/runs", f.writer, map[string]any{"request_id": "missing", "message": "generate my business SQL"})
		if status != 400 {
			t.Fatal("missing SQL accepted")
		}
		status, _, _ = f.request("POST", "/agent/runs", f.reader, map[string]any{"request_id": "reader", "sql": "SELECT id FROM orders"})
		if status != 403 {
			t.Fatal("reader created a model run")
		}
		r := submit(f.tenant + "_web_cancel")
		step()
		r = readRun(r.ID)
		if r.Job == nil {
			t.Fatal("child missing before cancellation")
		}
		status, _, _ = f.request("POST", "/agent/runs/"+r.ID+"/cancel", f.writer, nil)
		if status != 200 {
			t.Fatal("run cancellation failed")
		}
		step()
		r = readRun(r.ID)
		if r.Status != "cancelled" || r.StopReason != "cancelled" || r.Job == nil || r.Job.Status != "CANCELLED" {
			t.Fatalf("child cancellation not settled %s %s", r.Status, r.StopReason)
		}
	})
	t.Run("original_session_revoked_before_dispatch", func(t *testing.T) {
		r := submit(f.tenant + "_web_revoke")
		before := model.calls
		_, err := f.control.Exec("UPDATE sql_sentinel_sessions SET revoked_at=UTC_TIMESTAMP(6) WHERE token_id=?", f.writerSession)
		if err != nil {
			t.Fatal(err)
		}
		step()
		fresh, _ := f.issue("writer", "writer", f.tenant, f.project, []string{"orders"})
		status, raw, _ := f.request("GET", "/agent/runs/"+r.ID, fresh, nil)
		var out agentrun.Run
		_ = json.Unmarshal(raw, &out)
		if status != 200 || out.Status != "failed" || out.StopReason != "authorization_failed" || model.calls != before || out.ModelCalls != 0 {
			t.Fatal("revoked original session dispatched or lost explanatory terminal state")
		}
	})
}

// Keep this fixture's inner path explicit: it uses the existing Runner and
// real disposable MySQL, not an HTTP mock that manufactures successful plans.
var _ agentgraph.DraftNode = integrationFinishNode()
