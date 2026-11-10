package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"sqlsentinel/internal/agentgraph"
	"sqlsentinel/internal/candidate"
	"sqlsentinel/internal/historicalprior"
	"sqlsentinel/internal/identity"
	workflow "sqlsentinel/internal/kafka"
)

// This entry only accepts explicitly supplied private DSN files and known
// disposable schemas. No real model, gateway, SMTP, Kafka broker or production
// database is contacted. The queue boundary below is a real durable outbox
// event delivered directly to Worker.Handle, not a broker acceptance claim.
type agentIntegration struct {
	t                                                        *testing.T
	control, baseline, candidate                             *sql.DB
	store                                                    *workflow.MySQLStore
	auth                                                     *identity.Auth
	server                                                   *httptest.Server
	tenant, project, writer, writerSession, reader, reviewer string
	sequence                                                 int
}

func openAgentIntegration(t *testing.T) *agentIntegration {
	t.Helper()
	envs := []string{"SQL_SENTINEL_AGENT_CONTROL_DSN_FILE", "SQL_SENTINEL_AGENT_BASELINE_DSN_FILE", "SQL_SENTINEL_AGENT_CANDIDATE_DSN_FILE"}
	for _, name := range envs {
		if os.Getenv(name) == "" {
			t.Skip("explicit isolated Agent MySQL DSN files required")
		}
	}
	schemas := []string{"sentinel_improvement", "agent_baseline", "agent_candidate"}
	dbs := make([]*sql.DB, 3)
	for i, name := range envs {
		file := os.Getenv(name)
		if !filepath.IsAbs(file) {
			t.Fatal("Agent DSN file must be an absolute private path")
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal("cannot read the explicitly selected Agent DSN file")
		}
		cfg, err := mysql.ParseDSN(strings.TrimSpace(string(raw)))
		if err != nil || cfg.DBName != schemas[i] || !cfg.ParseTime {
			t.Fatal("Agent integration refuses an unexpected schema or DSN mode")
		}
		db, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			t.Fatal("cannot open isolated Agent database")
		}
		dbs[i] = db
		db.SetMaxOpenConns(12)
		db.SetMaxIdleConns(6)
		t.Cleanup(func() { db.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var actual string
		err = db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&actual)
		cancel()
		if err != nil || actual != schemas[i] {
			t.Fatal("isolated Agent database identity could not be verified")
		}
	}
	f := &agentIntegration{t: t, control: dbs[0], baseline: dbs[1], candidate: dbs[2], tenant: fmt.Sprintf("agent_%d", time.Now().UnixNano()), project: "diagnosis"}
	f.store = &workflow.MySQLStore{DB: f.control}
	if err := f.store.EnsureSchema(context.Background()); err != nil {
		t.Fatal("isolated Agent schema preparation failed")
	}
	var err error
	f.auth, err = identity.New(f.control, bytes.Repeat([]byte("k"), 32), strings.Repeat("fixture-bootstrap-", 3))
	if err != nil {
		t.Fatal("test identity setup failed")
	}
	f.writer, f.writerSession = f.issue("writer", "writer", f.tenant, f.project, []string{"orders"})
	f.reader, _ = f.issue("reader", "reader", f.tenant, f.project, []string{"orders"})
	f.reviewer, _ = f.issue("reviewer", "reviewer", f.tenant, f.project, []string{"orders"})
	f.server = httptest.NewServer((&Server{Store: f.store, Auth: f.auth, AllowedTables: map[string]bool{"orders": true}}).Handler())
	t.Cleanup(f.server.Close)
	return f
}

func (f *agentIntegration) issue(subject, role, tenant, project string, tables []string) (string, string) {
	f.t.Helper()
	token, id, err := f.auth.Issue(context.Background(), identity.Principal{Subject: subject, Role: role, Tenant: tenant, Project: project, Tables: tables})
	if err != nil {
		f.t.Fatal("test scoped session issuance failed")
	}
	return token, id
}

func (f *agentIntegration) resetOrders() {
	f.t.Helper()
	for _, db := range []*sql.DB{f.baseline, f.candidate} {
		if _, err := db.Exec("DROP TABLE IF EXISTS orders"); err != nil {
			f.t.Fatal("isolated fixture reset failed")
		}
		if _, err := db.Exec("CREATE TABLE orders(id BIGINT NOT NULL PRIMARY KEY,user_id BIGINT NOT NULL,status VARCHAR(32) NOT NULL,payload VARCHAR(1024) NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin"); err != nil {
			f.t.Fatal("isolated orders schema creation failed")
		}
		tx, err := db.Begin()
		if err != nil {
			f.t.Fatal("fixture transaction failed")
		}
		statement, err := tx.Prepare("INSERT INTO orders(id,user_id,status,payload) VALUES (?,?,?,?)")
		if err != nil {
			tx.Rollback()
			f.t.Fatal("fixture statement preparation failed")
		}
		for i := 1; i <= 128; i++ {
			if _, err = statement.Exec(i, i%64, "paid", "needle"); err != nil {
				statement.Close()
				tx.Rollback()
				f.t.Fatal("fixture row insertion failed")
			}
		}
		statement.Close()
		if tx.Commit() != nil {
			f.t.Fatal("fixture transaction commit failed")
		}
	}
}

func (f *agentIntegration) request(method, path, token string, body any) (int, []byte, http.Header) {
	f.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.server.URL+path, reader)
	if err != nil {
		f.t.Fatal("test HTTP construction failed")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := f.server.Client().Do(req)
	if err != nil {
		f.t.Fatal("isolated task HTTP request failed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 3<<20))
	if err != nil {
		f.t.Fatal("task HTTP response read failed")
	}
	return response.StatusCode, raw, response.Header
}

func (f *agentIntegration) submit(mode string, spec any, delivery string) workflow.Job {
	f.t.Helper()
	if delivery == "" {
		f.sequence++
		delivery = fmt.Sprintf("%s_%d", f.tenant, f.sequence)
	}
	body := map[string]any{"delivery_id": delivery, "pr_number": 1, "sql": "SELECT id FROM orders WHERE user_id=42 AND payload='needle'"}
	if mode != "" {
		body["execution_mode"] = mode
	}
	if spec != nil {
		body["candidate_spec"] = spec
	}
	status, raw, _ := f.request("POST", "/jobs", f.writer, body)
	if status != 202 {
		f.t.Fatalf("task submit status=%d code=%s", status, safeIntegrationCode(raw))
	}
	job, err := f.store.Get(context.Background(), delivery)
	if err != nil {
		f.t.Fatal("submitted durable task missing")
	}
	return job
}

func safeIntegrationCode(raw []byte) string {
	var result struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &result)
	return result.Code
}
func (f *agentIntegration) runner(node agentgraph.DraftNode) Runner {
	return Runner{Control: f.control, Baseline: f.baseline, Candidate: f.candidate, Model: node, AllowedTables: map[string]bool{"orders": true}, ModelVersion: "local-protocol-fixture"}
}
func (f *agentIntegration) run(job workflow.Job, node agentgraph.DraftNode) {
	f.t.Helper()
	var raw []byte
	if f.control.QueryRow("SELECT payload FROM sql_sentinel_outbox WHERE job_id=? ORDER BY created_at,event_id LIMIT 1", job.ID).Scan(&raw) != nil {
		f.t.Fatal("durable outbox event missing")
	}
	event, err := workflow.DecodeEvent(raw)
	if err != nil || event.JobID != job.ID || bytes.Contains(raw, job.SQL) {
		f.t.Fatal("outbox did not preserve opaque task boundary")
	}
	runner := f.runner(node)
	worker := workflow.Worker{Store: f.store, Runner: runner.Run, Config: workflow.Config{MaxAttempts: 1, Lease: 5 * time.Minute}}
	if err := worker.Handle(context.Background(), workflow.Message{Value: raw, Topic: "fixture-durable-outbox"}); err != nil {
		f.t.Fatal("isolated worker execution failed")
	}
}

type integrationBundle struct {
	JobID        string                `json:"job_id"`
	Mode         string                `json:"mode"`
	Outcome      string                `json:"outcome"`
	Stop         string                `json:"stop_reason"`
	Candidate    *candidate.Spec       `json:"candidate_spec"`
	CandidateSHA string                `json:"candidate_spec_sha256"`
	Agent        agentgraph.LoopResult `json:"agent"`
	Usage        workflow.AgentUsage   `json:"agent_usage"`
}

func (f *agentIntegration) report(job workflow.Job) (integrationBundle, []byte, string) {
	f.t.Helper()
	status, raw, headers := f.request("GET", "/jobs/by-id/"+job.ID+"/report", f.writer, nil)
	if status != 200 {
		f.t.Fatalf("durable report status=%d code=%s", status, safeIntegrationCode(raw))
	}
	var bundle integrationBundle
	if json.Unmarshal(raw, &bundle) != nil || bundle.JobID != job.ID {
		f.t.Fatal("report is not bound to the durable task")
	}
	return bundle, raw, headers.Get("X-Sentinel-Report-SHA256")
}
func (f *agentIntegration) assertClean() {
	f.t.Helper()
	var indexes int
	if f.candidate.QueryRow("SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND INDEX_NAME LIKE 'idx\\_cand\\_%'").Scan(&indexes) != nil || indexes != 0 {
		f.t.Fatal("candidate indexes leaked after verifier return")
	}
	conn, err := f.candidate.Conn(context.Background())
	if err != nil {
		f.t.Fatal("cleanup connection unavailable")
	}
	defer conn.Close()
	var locked sql.NullInt64
	if conn.QueryRowContext(context.Background(), "SELECT GET_LOCK(CONCAT('sentinel-shadow:',DATABASE()),0)").Scan(&locked) != nil || !locked.Valid || locked.Int64 != 1 {
		f.t.Fatal("shadow named lock was not released")
	}
	var released sql.NullInt64
	if conn.QueryRowContext(context.Background(), "SELECT RELEASE_LOCK(CONCAT('sentinel-shadow:',DATABASE()))").Scan(&released) != nil || released.Int64 != 1 {
		f.t.Fatal("test named lock release failed")
	}
}

type realAgentModelFixture struct {
	run func(context.Context, string) (string, error)
}

func (m realAgentModelFixture) Draft(ctx context.Context, prompt string) (string, error) {
	return m.run(ctx, prompt)
}
func (m realAgentModelFixture) DraftWithSystem(ctx context.Context, rules, prompt string) (string, error) {
	if rules != agentgraph.AgentRules {
		return "", errors.New("fixture_requires_separate_system_policy")
	}
	return m.run(ctx, prompt)
}

type realAgentPrompt struct {
	Task         agentgraph.LoopInput     `json:"task"`
	Observations []agentgraph.Observation `json:"observations"`
}

func integrationAction(action string, column string, observation *agentgraph.Observation) string {
	a := agentgraph.Action{Action: action}
	if action == "evaluate_index" {
		a.Candidate = &candidate.Spec{Table: "orders", IndexName: "idx_cand_proposal", Columns: []candidate.Column{{Name: column, Direction: "ASC"}}}
	}
	if action == "finish" {
		a.Summary = "Local L1 plan observation; no measured performance claim."
		if observation != nil {
			a.SelectedCandidateID = observation.ID
			a.EvidenceIDs = []string{observation.ID}
		}
	}
	raw, _ := json.Marshal(a)
	return string(raw)
}

func fixtureChoice(t *testing.T, prompt string, adjust bool) string {
	t.Helper()
	var p realAgentPrompt
	if json.Unmarshal([]byte(prompt), &p) != nil || len(p.Observations) == 0 {
		t.Fatal("model fixture received unstructured evidence")
	}
	if len(p.Observations) == 1 {
		column := "user_id"
		if adjust {
			column = "payload"
		}
		return integrationAction("evaluate_index", column, nil)
	}
	last := p.Observations[len(p.Observations)-1]
	if last.Status == "rejected" && last.Code == "candidate_key_too_long" {
		return integrationAction("evaluate_index", "user_id", nil)
	}
	if last.Verified {
		return integrationAction("finish", "", &last)
	}
	t.Fatalf("unexpected verifier observation: status=%s code=%s", last.Status, last.Code)
	return ""
}

func compatibleFixtureNode(t *testing.T, adjust bool) (agentgraph.DraftNode, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages  []struct{ Role, Content string }
			MaxTokens int  `json:"max_tokens"`
			Stream    bool `json:"stream"`
		}
		if r.URL.Path != "/v1/chat/completions" || r.Method != "POST" || json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&input) != nil || len(input.Messages) != 2 || input.Messages[0].Role != "system" || input.Messages[0].Content != agentgraph.AgentRules || input.Messages[1].Role != "user" || input.MaxTokens != 1024 || !input.Stream {
			t.Error("actual model HTTP request violates policy separation or token cap")
			http.Error(w, "invalid fixture request", 400)
			return
		}
		calls.Add(1)
		text := fixtureChoice(t, input.Messages[1].Content, adjust)
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := map[string]any{"id": "local-agent-fixture", "object": "chat.completion.chunk", "model": "fixture-only", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": text}, "finish_reason": nil}}}
		raw, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", raw)
	}))
	t.Cleanup(server.Close)
	node, err := agentgraph.NewOpenAINode(context.Background(), "fixture-not-a-provider-secret", "fixture-only", server.URL+"/v1")
	if err != nil {
		t.Fatal("local model adapter construction failed")
	}
	node, err = agentgraph.NewEinoGraph(node)
	if err != nil {
		t.Fatal("Eino model wrapper construction failed")
	}
	return node, calls
}

func (f *agentIntegration) claim(job workflow.Job, owner string) workflow.Job {
	f.t.Helper()
	claimed, ok, err := f.store.Claim(context.Background(), job.ID, owner, time.Now().UTC(), 5*time.Minute)
	if err != nil || !ok {
		f.t.Fatal("isolated task claim failed")
	}
	return claimed
}
func (f *agentIntegration) beginWorker(job workflow.Job, node agentgraph.DraftNode) <-chan error {
	f.t.Helper()
	var raw []byte
	if f.control.QueryRow("SELECT payload FROM sql_sentinel_outbox WHERE job_id=? ORDER BY created_at,event_id LIMIT 1", job.ID).Scan(&raw) != nil {
		f.t.Fatal("worker fixture event missing")
	}
	runner := f.runner(node)
	worker := workflow.Worker{Store: f.store, Runner: runner.Run, Config: workflow.Config{MaxAttempts: 1, Lease: 5 * time.Minute}}
	done := make(chan error, 1)
	go func() { done <- worker.Handle(context.Background(), workflow.Message{Value: raw}) }()
	return done
}
func awaitIntegration(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("asynchronous isolated worker failed")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("isolated worker failed to stop within cancellation boundary")
	}
}

func integrationFinishNode() agentgraph.DraftNode {
	return realAgentModelFixture{run: func(context.Context, string) (string, error) { return integrationAction("finish", "", nil), nil }}
}

func TestAgentRealMySQLIntegration(t *testing.T) {
	f := openAgentIntegration(t)
	t.Run("http_outbox_worker_observe_adjust_and_approval", func(t *testing.T) {
		f.t = t
		f.resetOrders()
		node, calls := compatibleFixtureNode(t, true)
		job := f.submit("agent.v1", nil, "")
		f.run(job, node)
		bundle, _, sha := f.report(job)
		if bundle.Mode != "agent" || bundle.Outcome != "recommended" || bundle.Candidate == nil || bundle.Candidate.Columns[0].Name != "user_id" || bundle.Usage.ModelCalls != 3 || bundle.Usage.Evaluations != 2 || calls.Load() != 3 || bundle.Agent.EligibleForPerformanceClaim {
			t.Fatalf("actual closed loop failed: outcome=%s stop=%s model=%d eval=%d", bundle.Outcome, bundle.Stop, bundle.Usage.ModelCalls, bundle.Usage.Evaluations)
		}
		if len(bundle.Agent.Observations) != 3 || bundle.Agent.Observations[1].Code != "candidate_key_too_long" || bundle.Agent.Observations[1].Verified || !bundle.Agent.Observations[2].Verified {
			t.Fatal("second proposal was not grounded in actual failed verification")
		}
		f.assertClean()
		status, _, _ := f.request("POST", "/jobs/by-id/"+job.ID+"/approval", f.reviewer, map[string]any{"report_sha256": sha, "decision": "approved", "reason": "Fixture review of verified L1 candidate; measurement remains separate."})
		if status != 201 {
			t.Fatalf("Agent report approval status=%d", status)
		}
		delivery := job.DeliveryID + "_benchmark"
		status, raw, _ := f.request("POST", "/jobs/by-id/"+job.ID+"/benchmark", f.reviewer, map[string]any{"report_sha256": sha, "delivery_id": delivery})
		if status != 202 {
			t.Fatalf("approved benchmark submit status=%d code=%s", status, safeIntegrationCode(raw))
		}
		bench, err := f.store.Get(context.Background(), delivery)
		if err != nil || !bench.Options.Benchmark || bench.Options.AgentMode != "" || bench.Options.AgentVersion != "" {
			t.Fatal("benchmark task inherited autonomous execution mode")
		}
	})
	t.Run("first_candidate_and_no_change", func(t *testing.T) {
		f.t = t
		f.resetOrders()
		node, calls := compatibleFixtureNode(t, false)
		job := f.submit("agent.v1", nil, "")
		f.run(job, node)
		bundle, _, _ := f.report(job)
		if bundle.Outcome != "recommended" || calls.Load() != 2 || bundle.Usage.Evaluations != 1 {
			t.Fatal("first valid candidate did not finish in two decisions")
		}
		f.assertClean()
		job = f.submit("agent.v1", nil, "")
		f.run(job, realAgentModelFixture{run: func(context.Context, string) (string, error) { return integrationAction("finish", "", nil), nil }})
		bundle, _, sha := f.report(job)
		if bundle.Outcome != "no_change" || bundle.Candidate != nil || bundle.Usage.ModelCalls != 1 || bundle.Usage.Evaluations != 0 {
			t.Fatal("no candidate is not a valid terminal business result")
		}
		status, _, _ := f.request("POST", "/jobs/by-id/"+job.ID+"/approval", f.reviewer, map[string]any{"report_sha256": sha, "decision": "approved", "reason": "must be refused"})
		if status != 409 {
			t.Fatal("an absent candidate could be approved")
		}
		f.assertClean()
	})

	t.Run("durable_reservations_concurrency_retry_and_stale_fences", func(t *testing.T) {
		f.t = t
		job := f.claim(f.submit("agent.v1", nil, ""), "reservation_attempt_1")
		reserve := func(kind string, count, want int) {
			done := make(chan error, count)
			for i := 0; i < count; i++ {
				go func() { done <- f.store.ReserveAgent(context.Background(), job, kind) }()
			}
			accepted := 0
			for i := 0; i < count; i++ {
				err := <-done
				if err == nil {
					accepted++
				} else if !errors.Is(err, workflow.ErrAgentBudget) {
					t.Fatal("unexpected durable reservation database error")
				}
			}
			if accepted != want {
				t.Fatalf("reservation accepted %d, want %d", accepted, want)
			}
		}
		reserve("model", 16, 5)
		reserve("evaluation", 12, 2)
		usage, err := f.store.AgentUsage(context.Background(), job.ID)
		if err != nil || usage.ModelCalls != 5 || usage.Evaluations != 2 {
			t.Fatal("concurrent budget exceeded or lost reservations")
		}
		now := time.Now().UTC()
		ok, err := f.store.Retry(context.Background(), job.ID, job.LeaseOwner, now, now, "isolated_retry")
		if err != nil || !ok {
			t.Fatal("durable retry transition failed")
		}
		next := f.claim(job, "reservation_attempt_2")
		if next.Attempts != 2 || !errors.Is(f.store.ReserveAgent(context.Background(), next, "model"), workflow.ErrAgentBudget) || !errors.Is(f.store.ReserveAgent(context.Background(), next, "evaluation"), workflow.ErrAgentBudget) {
			t.Fatal("durable retry reset the task budget")
		}
		if !errors.Is(f.store.ReserveAgent(context.Background(), job, "model"), workflow.ErrLeaseLost) {
			t.Fatal("stale owner reserved budget")
		}
		staleAttempt := next
		staleAttempt.Attempts = 1
		if !errors.Is(f.store.CheckAgent(context.Background(), staleAttempt), workflow.ErrLeaseLost) {
			t.Fatal("current owner with stale attempt passed fence")
		}
		forged := next
		forged.Options.TenantID = "foreign"
		if !errors.Is(f.store.ReserveAgent(context.Background(), forged, "model"), workflow.ErrAgentAuthorization) {
			t.Fatal("forged task options passed durable authority check")
		}
		marker := []byte(`{"job_id":"` + job.ID + `","marker":"new_attempt_only"}`)
		if _, err = f.control.Exec("INSERT INTO sql_sentinel_reports(job_id,report) VALUES (?,?)", job.ID, marker); err != nil {
			t.Fatal("stale-report fixture preparation failed")
		}
		runner := f.runner(integrationFinishNode())
		if runner.Run(context.Background(), job) == nil {
			t.Fatal("old owner wrote a report")
		}
		var raw []byte
		if f.control.QueryRow("SELECT report FROM sql_sentinel_reports WHERE job_id=?", job.ID).Scan(&raw) != nil || !bytes.Contains(raw, []byte("new_attempt_only")) {
			t.Fatal("old execution overwrote the new report")
		}
		terminalized, err := f.store.Cancel(context.Background(), next, "writer")
		if err != nil || !terminalized {
			t.Fatal("reservation fixture cleanup failed")
		}
	})

	t.Run("idempotent_replay_does_not_replace_original_execution_authority", func(t *testing.T) {
		f.t = t
		job := f.submit("agent.v1", nil, "")
		renewed, _ := f.issue("writer", "writer", f.tenant, f.project, []string{"orders"})
		body := map[string]any{"delivery_id": job.DeliveryID, "pr_number": 1, "sql": string(job.SQL), "execution_mode": "agent.v1"}
		status, raw, _ := f.request("POST", "/jobs", renewed, body)
		var replay struct {
			ID        string `json:"job_id"`
			Duplicate bool   `json:"duplicate"`
		}
		if status != 202 || json.Unmarshal(raw, &replay) != nil || !replay.Duplicate || replay.ID != job.ID {
			t.Fatal("renewed session duplicated an idempotent task")
		}
		stored, err := f.store.GetByID(context.Background(), job.ID)
		if err != nil || stored.Options.AuthorizationSession != job.Options.AuthorizationSession {
			t.Fatal("idempotent replay silently replaced immutable execution authority")
		}
		body["execution_mode"] = ""
		status, _, _ = f.request("POST", "/jobs", renewed, body)
		if status != 409 {
			t.Fatal("idempotent replay changed a task execution mode")
		}
		for _, extra := range []string{"agent_decisions", "agent_evaluations", "tenant_id"} {
			body["delivery_id"] = job.DeliveryID + "_unknown"
			body["execution_mode"] = "agent.v1"
			body[extra] = 999
			status, _, _ = f.request("POST", "/jobs", renewed, body)
			delete(body, extra)
			if status != 400 {
				t.Fatal("caller supplied a trusted task budget or identity field")
			}
		}
		status, _, _ = f.request("POST", "/jobs/by-id/"+job.ID+"/cancel", f.writer, map[string]any{})
		if status != 200 {
			t.Fatal("idempotency fixture could not be terminalized")
		}
	})

	t.Run("optional_history_http_failure_is_not_an_empty_authorized_result", func(t *testing.T) {
		f.t = t
		f.resetOrders()
		for _, upstream := range []int{503, 401} {
			t.Run(fmt.Sprint(upstream), func(t *testing.T) {
				f.t = t
				var requests atomic.Int32
				historyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(upstream)
					_, _ = w.Write([]byte(`{"error":"fixture-only"}`))
				}))
				defer historyServer.Close()
				history, err := historicalprior.New(historyServer.URL, strings.Repeat("h", 32), "")
				if err != nil {
					t.Fatal("history HTTP fixture configuration failed")
				}
				var calls atomic.Int32
				node := realAgentModelFixture{run: func(_ context.Context, prompt string) (string, error) {
					n := calls.Add(1)
					var p realAgentPrompt
					if json.Unmarshal([]byte(prompt), &p) != nil || !p.Task.AllowHistory {
						t.Fatal("configured history capability not described accurately")
					}
					if n == 1 {
						raw, _ := json.Marshal(agentgraph.Action{Action: "search_history", Table: "orders"})
						return string(raw), nil
					}
					last := p.Observations[len(p.Observations)-1]
					if last.Status != "unavailable" {
						t.Fatal("optional HTTP outage was hidden as an empty successful search")
					}
					return integrationAction("finish", "", nil), nil
				}}
				job := f.submit("agent.v1", nil, "")
				var event []byte
				if f.control.QueryRow("SELECT payload FROM sql_sentinel_outbox WHERE job_id=? LIMIT 1", job.ID).Scan(&event) != nil {
					t.Fatal("history fixture event missing")
				}
				runner := f.runner(node)
				runner.History = history
				worker := workflow.Worker{Store: f.store, Runner: runner.Run, Config: workflow.Config{MaxAttempts: 1, Lease: 5 * time.Minute}}
				if worker.Handle(context.Background(), workflow.Message{Value: event}) != nil {
					t.Fatal("history worker execution failed")
				}
				bundle, _, _ := f.report(job)
				if requests.Load() != 1 {
					t.Fatal("history fixture was not actually called")
				}
				if upstream == 503 && (bundle.Outcome != "no_change" || calls.Load() != 2) {
					t.Fatal("optional dependency outage blocked an independent diagnosis")
				}
				if upstream == 401 && (bundle.Outcome != "authorization_failed" || calls.Load() != 1) {
					t.Fatal("historical authorization failure was treated as optional empty data")
				}
				f.assertClean()
			})
		}
	})

	t.Run("context_changes_are_infrastructure_not_no_gain", func(t *testing.T) {
		f.t = t
		for _, change := range []string{"initial_candidate_schema", "initial_candidate_data", "both_snapshots_after_verification", "both_schemas_after_verification"} {
			t.Run(change, func(t *testing.T) {
				f.t = t
				f.resetOrders()
				var calls atomic.Int32
				if change == "initial_candidate_schema" {
					if _, err := f.candidate.Exec("ALTER TABLE orders ADD COLUMN context_extra INT NULL"); err != nil {
						t.Fatal("schema mismatch fixture failed")
					}
				}
				if change == "initial_candidate_data" {
					if _, err := f.candidate.Exec("UPDATE orders SET payload='different' WHERE id=1"); err != nil {
						t.Fatal("snapshot mismatch fixture failed")
					}
				}
				node := realAgentModelFixture{run: func(_ context.Context, prompt string) (string, error) {
					n := calls.Add(1)
					if n == 2 {
						q := "UPDATE orders SET payload='new_snapshot' WHERE id=1"
						if change == "both_schemas_after_verification" {
							q = "ALTER TABLE orders ADD COLUMN context_extra INT NULL"
						}
						for _, db := range []*sql.DB{f.baseline, f.candidate} {
							if _, err := db.Exec(q); err != nil {
								return "", errors.New("fixture_mutation_failed")
							}
						}
					}
					return fixtureChoice(t, prompt, false), nil
				}}
				job := f.submit("agent.v1", nil, "")
				f.run(job, node)
				bundle, _, _ := f.report(job)
				if bundle.Outcome != "infrastructure_failed" || bundle.Candidate != nil || bundle.Agent.Selected != nil {
					t.Fatalf("context change misclassified: outcome=%s stop=%s", bundle.Outcome, bundle.Stop)
				}
				if strings.HasPrefix(change, "initial_") && calls.Load() != 0 {
					t.Fatal("model ran before matching initial evidence was established")
				}
				f.assertClean()
			})
		}
	})

	t.Run("oversized_valid_schema_is_context_budget_not_infrastructure", func(t *testing.T) {
		f.t = t
		f.resetOrders()
		defer f.resetOrders()
		// A valid ENUM definition enlarges schema evidence without adding rows or
		// imposing an artificial database failure. Both shadows remain identical.
		values := make([]string, 3000)
		for i := range values {
			values[i] = fmt.Sprintf("'v%04d'", i)
		}
		ddl := "ALTER TABLE orders ADD COLUMN context_enum ENUM(" + strings.Join(values, ",") + ") NOT NULL DEFAULT 'v0000'"
		for _, db := range []*sql.DB{f.baseline, f.candidate} {
			if _, err := db.Exec(ddl); err != nil {
				t.Fatal("valid oversized-schema fixture creation failed")
			}
		}
		var modelCalls atomic.Int32
		node := realAgentModelFixture{run: func(context.Context, string) (string, error) {
			modelCalls.Add(1)
			return integrationAction("finish", "", nil), nil
		}}
		job := f.submit("agent.v1", nil, "")
		f.run(job, node)
		bundle, _, _ := f.report(job)
		stored, err := f.store.GetByID(context.Background(), job.ID)
		if err != nil || stored.Status != workflow.Completed || bundle.Outcome != "budget_exhausted" || bundle.Candidate != nil || bundle.Agent.Selected != nil || bundle.Usage.ModelCalls != 0 || bundle.Usage.Evaluations != 0 || modelCalls.Load() != 0 {
			t.Fatalf("valid schema context cap misclassified: status=%s outcome=%s stop=%s model_calls=%d", stored.Status, bundle.Outcome, bundle.Stop, modelCalls.Load())
		}
		f.assertClean()
		f.resetOrders()
		var fixedCalls atomic.Int32
		spec := candidate.Spec{Table: "orders", IndexName: "idx_cand_context_control", Columns: []candidate.Column{{Name: "user_id", Direction: "ASC"}}}
		fixed := realAgentModelFixture{run: func(context.Context, string) (string, error) {
			fixedCalls.Add(1)
			raw, _ := json.Marshal(spec)
			return string(raw), nil
		}}
		control := f.submit("", nil, "")
		f.run(control, fixed)
		controlReport, _, _ := f.report(control)
		if controlReport.Mode != "" || controlReport.Candidate == nil || fixedCalls.Load() != 1 {
			t.Fatal("normal fixed-flow control failed after restoring the schema")
		}
		f.assertClean()
	})

	t.Run("explicit_ids_collision_and_scope_cancel", func(t *testing.T) {
		f.t = t
		f.resetOrders()
		a := f.submit("agent.v1", nil, "")
		b := f.submit("agent.v1", nil, a.ID)
		for _, entry := range []struct{ path, want string }{{"/jobs/by-id/" + a.ID, a.ID}, {"/jobs/" + a.ID, b.ID}} {
			status, raw, _ := f.request("GET", entry.path, f.writer, nil)
			var value struct {
				ID string `json:"job_id"`
			}
			if status != 200 || json.Unmarshal(raw, &value) != nil || value.ID != entry.want {
				t.Fatal("explicit job-id and delivery-id namespaces collided")
			}
		}
		for _, scope := range []struct {
			name, tenant, project, actor, role string
			tables                             []string
		}{{"tenant", f.tenant + "_foreign", f.project, "writer", "writer", []string{"orders"}}, {"project", f.tenant, "foreign", "writer", "writer", []string{"orders"}}, {"table", f.tenant, f.project, "writer", "writer", []string{"secrets"}}, {"actor", f.tenant, f.project, "someone_else", "writer", []string{"orders"}}, {"reader", f.tenant, f.project, "reader", "reader", []string{"orders"}}} {
			token, _ := f.issue(scope.actor, scope.role, scope.tenant, scope.project, scope.tables)
			status, _, _ := f.request("POST", "/jobs/by-id/"+a.ID+"/cancel", token, map[string]any{})
			if status != 403 && status != 404 {
				t.Fatalf("foreign %s cancellation status=%d", scope.name, status)
			}
		}
		status, _, _ := f.request("POST", "/jobs/by-id/"+a.ID+"/cancel", f.writer, map[string]any{})
		if status != 200 {
			t.Fatal("authorized explicit job-id cancellation failed")
		}
		still, err := f.store.Get(context.Background(), b.DeliveryID)
		if err != nil || still.ID != b.ID || still.Status != workflow.Queued {
			t.Fatal("job-id cancellation accidentally cancelled colliding delivery")
		}
		status, _, _ = f.request("GET", "/jobs/"+a.ID+"/report", f.writer, nil)
		if status != 404 {
			t.Fatal("delivery report lookup returned another task's cancellation report")
		}
		status, _, _ = f.request("POST", "/jobs/"+a.ID+"/cancel", f.writer, map[string]any{})
		if status != 200 {
			t.Fatal("colliding delivery cancellation failed")
		}
		for _, entry := range []struct{ path, want string }{{"/jobs/by-id/" + a.ID + "/report", a.ID}, {"/jobs/" + a.ID + "/report", b.ID}} {
			status, raw, _ := f.request("GET", entry.path, f.writer, nil)
			var value integrationBundle
			if status != 200 || json.Unmarshal(raw, &value) != nil || value.JobID != entry.want {
				t.Fatal("report namespace resolved to the wrong durable task")
			}
		}
	})

	t.Run("literal_by_id_delivery_and_queued_agent_cancel_report", func(t *testing.T) {
		f.t = t
		if _, err := f.store.Get(context.Background(), "by-id"); !errors.Is(err, workflow.ErrNotFound) {
			t.Fatal("literal delivery fixture is already occupied; refusing to replace another task")
		}
		job := f.submit("agent.v1", nil, "by-id")
		t.Cleanup(func() {
			for _, table := range []string{"sql_sentinel_reports", "sql_sentinel_agent_usage", "sql_sentinel_approvals", "sql_sentinel_sources", "sql_sentinel_job_history", "sql_sentinel_outbox", "sql_sentinel_job_options", "sql_sentinel_jobs"} {
				if _, err := f.control.Exec("DELETE FROM "+table+" WHERE job_id=?", job.ID); err != nil {
					t.Error("literal delivery fixture cleanup failed")
				}
			}
		})
		status, raw, _ := f.request("GET", "/jobs/by-id", f.writer, nil)
		var state struct {
			ID string `json:"job_id"`
		}
		if status != 200 || json.Unmarshal(raw, &state) != nil || state.ID != job.ID {
			t.Fatal("legacy literal by-id delivery status was parsed as an ID namespace")
		}
		status, _, _ = f.request("GET", "/jobs/by-id/report", f.writer, nil)
		if status != 404 {
			t.Fatal("queued literal delivery report was not scoped/not-ready")
		}
		status, _, _ = f.request("POST", "/jobs/by-id/cancel", f.writer, map[string]any{})
		if status != 200 {
			t.Fatal("legacy literal by-id delivery cancel was parsed as an ID namespace")
		}
		status, raw, _ = f.request("GET", "/jobs/by-id/report", f.writer, nil)
		var bundle integrationBundle
		if status != 200 || json.Unmarshal(raw, &bundle) != nil || bundle.JobID != job.ID || bundle.Mode != "agent" || bundle.Agent.Version != agentgraph.AgentVersion || bundle.Agent.Status != "cancelled" || bundle.Outcome != "cancelled" {
			t.Fatal("queued Agent cancellation report lost its bound mode or stopping explanation")
		}
		var wire struct {
			Agent map[string]any `json:"agent"`
		}
		if json.Unmarshal(raw, &wire) != nil {
			t.Fatal("queued Agent cancellation report invalid")
		}
		if _, ok := wire.Agent["model_calls"]; ok {
			t.Fatal("unexecuted per-attempt model count was fabricated")
		}
		if bundle.Usage.ModelCalls != 0 || bundle.Usage.Evaluations != 0 {
			t.Fatal("queued cancellation did not retain actual zero durable reservations")
		}
	})

	t.Run("cancel_removes_stale_partial_candidate_selection", func(t *testing.T) {
		f.t = t
		job := f.submit("agent.v1", nil, "")
		spec := candidate.Spec{Table: "orders", IndexName: "idx_cand_partial", Columns: []candidate.Column{{Name: "user_id"}}}
		hash, _ := agentgraph.CanonicalCandidateHash(spec)
		partial := map[string]any{"job_id": job.ID, "mode": "agent", "version": "agent.v1", "outcome": "recommended", "candidate_spec": spec, "candidate_spec_sha256": strings.Repeat("f", 64), "agent": map[string]any{"status": "recommended", "summary": "old recommendation", "selected_candidate": spec, "selected_candidate_id": "old_candidate", "selected_candidate_sha256": hash}}
		raw, _ := json.Marshal(partial)
		if _, err := f.control.Exec("INSERT INTO sql_sentinel_reports(job_id,report) VALUES (?,?)", job.ID, raw); err != nil {
			t.Fatal("partial report fixture failed")
		}
		// A webhook source is a second task entry. Explicit cancellation must
		// terminalize its pending local comment record without sending a comment.
		if _, err := f.control.Exec("INSERT INTO sql_sentinel_sources(job_id,source,comment_state) VALUES (?,JSON_OBJECT('fixture',true),'pending')", job.ID); err != nil {
			t.Fatal("pending webhook source fixture failed")
		}
		status, _, _ := f.request("POST", "/jobs/by-id/"+job.ID+"/cancel", f.writer, map[string]any{})
		if status != 200 {
			t.Fatal("partial report cancellation failed")
		}
		bundle, raw, _ := f.report(job)
		if bundle.Outcome != "cancelled" || bundle.Candidate != nil || bundle.CandidateSHA != "" || bundle.Agent.Selected != nil || bundle.Agent.SelectedSHA256 != "" || bundle.Agent.SelectedCandidateID != "" {
			t.Fatal("cancelled partial report retained an approvable candidate")
		}
		if bytes.Contains(raw, []byte(`"selected_candidate"`)) || bytes.Contains(raw, []byte(`"candidate_spec_sha256"`)) || bytes.Contains(raw, []byte("old recommendation")) || bundle.Agent.Summary != "" {
			t.Fatal("selection/hash fields were not removed atomically")
		}
		var commentState string
		var terminalized bool
		if f.control.QueryRow("SELECT comment_state,comment_published_at IS NOT NULL FROM sql_sentinel_sources WHERE job_id=?", job.ID).Scan(&commentState, &terminalized) != nil || commentState != "cancelled" || !terminalized {
			t.Fatal("cancel left the webhook comment source pending")
		}
	})

	t.Run("active_authority_revocation_stops_and_persists_safe_result", func(t *testing.T) {
		f.t = t
		f.resetOrders()
		token, session := f.issue("revocable_writer", "writer", f.tenant, f.project, []string{"orders"})
		normal := f.writer
		f.writer = token
		job := f.submit("agent.v1", nil, "")
		f.writer = normal
		started := make(chan struct{})
		node := realAgentModelFixture{run: func(ctx context.Context, _ string) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}}
		done := f.beginWorker(job, node)
		select {
		case <-started:
		case <-time.After(4 * time.Second):
			t.Fatal("revocation fixture did not reach the model boundary")
		}
		if _, err := f.control.Exec("UPDATE sql_sentinel_sessions SET revoked_at=UTC_TIMESTAMP(6) WHERE token_id=?", session); err != nil {
			t.Fatal("isolated session revocation failed")
		}
		awaitIntegration(t, done)
		bundle, _, _ := f.report(job)
		if bundle.Outcome != "authorization_failed" || bundle.Candidate != nil || bundle.Usage.ModelCalls != 1 {
			t.Fatalf("revoked authority did not safely stop: outcome=%s", bundle.Outcome)
		}
		f.assertClean()
	})

	t.Run("authority_revoked_before_execution_has_a_safe_terminal_explanation", func(t *testing.T) {
		f.t = t
		f.resetOrders()
		token, session := f.issue("queued_revoked_writer", "writer", f.tenant, f.project, []string{"orders"})
		normal := f.writer
		f.writer = token
		job := f.submit("agent.v1", nil, "")
		f.writer = normal
		if _, err := f.control.Exec("UPDATE sql_sentinel_sessions SET revoked_at=UTC_TIMESTAMP(6) WHERE token_id=?", session); err != nil {
			t.Fatal("queued authority fixture revocation failed")
		}
		var calls atomic.Int32
		f.run(job, realAgentModelFixture{run: func(context.Context, string) (string, error) {
			calls.Add(1)
			return integrationAction("finish", "", nil), nil
		}})
		stored, err := f.store.GetByID(context.Background(), job.ID)
		if err != nil {
			t.Fatal("queued revoked task missing")
		}
		status, raw, _ := f.request("GET", "/jobs/by-id/"+job.ID+"/report", f.writer, nil)
		var bundle integrationBundle
		if stored.Status != workflow.Rejected || status != 200 || json.Unmarshal(raw, &bundle) != nil || bundle.Outcome != "authorization_failed" || bundle.Candidate != nil || calls.Load() != 0 {
			t.Fatalf("pre-execution revocation lacked a safe terminal reason: job=%s report_status=%d outcome=%s model_calls=%d", stored.Status, status, bundle.Outcome, calls.Load())
		}
		f.assertClean()
	})

	t.Run("named_lock_deadline_and_explicit_cancel_stop_execution", func(t *testing.T) {
		f.t = t
		f.resetOrders()
		hold, err := f.candidate.Conn(context.Background())
		if err != nil {
			t.Fatal("lock holder unavailable")
		}
		defer hold.Close()
		var locked int
		if hold.QueryRowContext(context.Background(), "SELECT GET_LOCK(CONCAT('sentinel-shadow:',DATABASE()),0)").Scan(&locked) != nil || locked != 1 {
			t.Fatal("lock fixture acquisition failed")
		}
		release := func() {
			var n int
			if hold.QueryRowContext(context.Background(), "SELECT RELEASE_LOCK(CONCAT('sentinel-shadow:',DATABASE()))").Scan(&n) != nil || n != 1 {
				t.Fatal("lock fixture release failed")
			}
		}
		node := realAgentModelFixture{run: func(context.Context, string) (string, error) {
			return integrationAction("evaluate_index", "user_id", nil), nil
		}}
		timeoutJob := f.claim(f.submit("agent.v1", nil, ""), "deadline_owner")
		runner := f.runner(node)
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		err = runner.Run(ctx, timeoutJob)
		cancel()
		if err != nil {
			t.Fatal("bounded lock deadline did not retain its partial timeout report")
		}
		var raw []byte
		if f.control.QueryRow("SELECT report FROM sql_sentinel_reports WHERE job_id=?", timeoutJob.ID).Scan(&raw) != nil {
			t.Fatal("timeout report missing")
		}
		var bundle integrationBundle
		if json.Unmarshal(raw, &bundle) != nil || bundle.Outcome != "timeout" || bundle.Candidate != nil {
			t.Fatal("lock timeout confused with no benefit or successful verification")
		}
		if ok, e := f.store.Complete(context.Background(), timeoutJob.ID, timeoutJob.LeaseOwner, time.Now().UTC()); e != nil || !ok {
			t.Fatal("timeout outcome could not be terminalized")
		}
		job := f.submit("agent.v1", nil, "")
		done := f.beginWorker(job, node)
		deadline := time.Now().Add(4 * time.Second)
		for {
			usage, e := f.store.AgentUsage(context.Background(), job.ID)
			if e != nil {
				t.Fatal("cancel reservation observation failed")
			}
			if usage.Evaluations == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("cancel fixture did not enter verification")
			}
			time.Sleep(20 * time.Millisecond)
		}
		status, _, _ := f.request("POST", "/jobs/by-id/"+job.ID+"/cancel", f.writer, map[string]any{})
		if status != 200 {
			t.Fatal("explicit active-task cancellation failed")
		}
		awaitIntegration(t, done)
		bundle, _, _ = f.report(job)
		if bundle.Outcome != "cancelled" || bundle.Candidate != nil {
			t.Fatal("cancelled running task did not expose a safe cancellation report")
		}
		release()
		f.assertClean()
	})

	t.Run("fixed_manual_and_rewrite_paths_stay_compatible", func(t *testing.T) {
		f.t = t
		f.resetOrders()
		spec := candidate.Spec{Table: "orders", IndexName: "idx_cand_manual", Columns: []candidate.Column{{Name: "user_id", Direction: "ASC"}}}
		var calls atomic.Int32
		fixed := realAgentModelFixture{run: func(context.Context, string) (string, error) {
			calls.Add(1)
			raw, _ := json.Marshal(spec)
			return string(raw), nil
		}}
		job := f.submit("", nil, "")
		f.run(job, fixed)
		bundle, _, _ := f.report(job)
		if bundle.Mode != "" || bundle.Candidate == nil || calls.Load() != 1 {
			t.Fatal("fixed auto-draft path changed")
		}
		f.assertClean()
		manual := f.submit("", spec, "")
		f.run(manual, realAgentModelFixture{run: func(context.Context, string) (string, error) {
			t.Error("manual candidate unexpectedly called a model")
			return "", errors.New("unexpected_model")
		}})
		bundle, _, _ = f.report(manual)
		if bundle.Mode != "" || bundle.Candidate == nil {
			t.Fatal("manual candidate compatibility failed")
		}
		f.assertClean()
		rewriteSpec := map[string]string{"kind": "rewrite", "sql": "SELECT id FROM orders WHERE payload='needle' AND user_id=42"}
		rewritten := f.submit("", rewriteSpec, "")
		f.run(rewritten, nil)
		status, raw, _ := f.request("GET", "/jobs/by-id/"+rewritten.ID+"/report", f.writer, nil)
		var wire struct {
			Mode    string `json:"mode"`
			Rewrite struct {
				Equivalent bool `json:"result_equivalent"`
			} `json:"rewrite"`
		}
		if status != 200 || json.Unmarshal(raw, &wire) != nil || wire.Mode != "" || !wire.Rewrite.Equivalent {
			t.Fatal("manual rewrite path lost its independent equivalence check")
		}
		f.assertClean()
	})
}
