package agentgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"sqlsentinel/internal/candidate"
	"sqlsentinel/internal/shadowdata"
)

type loopScript struct {
	run   func(context.Context, string, string) (string, error)
	calls int
}

func (n *loopScript) Draft(ctx context.Context, s string) (string, error) {
	return n.DraftWithSystem(ctx, "", s)
}
func (n *loopScript) DraftWithSystem(ctx context.Context, rules, s string) (string, error) {
	n.calls++
	return n.run(ctx, rules, s)
}

type loopToolFixture struct {
	input                                                       LoopInput
	checkErr, reserveErr, errorEvaluate, errorSearch            error
	modelReserved, evalReserved, evaluated, inspected, searched int
	observation                                                 func(candidate.Spec) Observation
}

func (f *loopToolFixture) Check(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return f.checkErr
}
func (f *loopToolFixture) Reserve(ctx context.Context, kind string) error {
	if f.reserveErr != nil {
		return f.reserveErr
	}
	if kind == "model" {
		f.modelReserved++
	} else if kind == "evaluation" {
		f.evalReserved++
	} else {
		panic("unknown reserve kind")
	}
	return ctx.Err()
}
func (f *loopToolFixture) Inspect(context.Context, string) (Observation, error) {
	f.inspected++
	return Observation{Status: "ok", Code: "schema_reused", Facts: json.RawMessage(`{"cached":true}`)}, nil
}
func (f *loopToolFixture) Search(context.Context, string) (Observation, error) {
	f.searched++
	return Observation{Status: "ok", Facts: json.RawMessage(`{"historical_only":true}`)}, f.errorSearch
}
func (f *loopToolFixture) Evaluate(_ context.Context, s candidate.Spec) (Observation, error) {
	f.evaluated++
	if f.errorEvaluate != nil {
		return Observation{}, f.errorEvaluate
	}
	if f.observation != nil {
		return f.observation(s), nil
	}
	hash, _ := CanonicalCandidateHash(s)
	s.IndexName = "idx_cand_server_owned"
	return Observation{ID: "forged-tool-id", Status: "ok", Verified: true, Candidate: &s, CandidateSHA256: hash, SnapshotSHA256: f.input.SnapshotSHA256, SchemaSHA256: f.input.SchemaSHA256, MySQLVersion: f.input.MySQLVersion, EvidenceLevel: "L1", Facts: json.RawMessage(`{"selected_index":true,"measured":false}`)}, nil
}
func loopTestInput() LoopInput {
	sql := "SELECT id FROM orders WHERE user_id=42"
	sum := sha256.Sum256([]byte(sql))
	return LoopInput{JobID: strings.Repeat("a", 32), Attempt: 1, SQL: sql, SQLSHA256: hex.EncodeToString(sum[:]), Tables: []string{"orders"}, Schema: []shadowdata.Table{{Name: "orders", Columns: []shadowdata.Column{{Name: "id", Type: "bigint", Nullable: "NO"}, {Name: "user_id", Type: "bigint", Nullable: "NO"}}}}, BaselinePlan: json.RawMessage(`{"query_block":{"table":{"access_type":"ALL"}}}`), SnapshotSHA256: strings.Repeat("1", 64), SchemaSHA256: strings.Repeat("2", 64), MySQLVersion: "8.4.11", MaxDecisions: 5, MaxEvaluations: 2}
}
func loopCandidate(name string) candidate.Spec {
	return candidate.Spec{Table: "orders", IndexName: name, Columns: []candidate.Column{{Name: "user_id", Direction: "ASC"}}}
}
func loopAction(a Action) string { raw, _ := json.Marshal(a); return string(raw) }
func loopFinish(prompt string) string {
	var p agentPrompt
	_ = json.Unmarshal([]byte(prompt), &p)
	o := p.Observations[len(p.Observations)-1]
	return loopAction(Action{Action: "finish", SelectedCandidateID: o.ID, EvidenceIDs: []string{o.ID}, Summary: "Verified plan evidence only."})
}

func TestLoopCompletesOnlyActuallyVerifiedCandidate(t *testing.T) {
	in := loopTestInput()
	tools := &loopToolFixture{input: in}
	node := &loopScript{run: func(_ context.Context, rules, prompt string) (string, error) {
		if rules != AgentRules {
			t.Fatal("trusted policy was not separated")
		}
		var p agentPrompt
		if json.Unmarshal([]byte(prompt), &p) != nil {
			t.Fatal("user prompt is not structured JSON")
		}
		if len(p.Observations) == 1 {
			return loopAction(Action{Action: "evaluate_index", Candidate: ptrLoopSpec(loopCandidate("idx_cand_model"))}), nil
		}
		return loopFinish(prompt), nil
	}}
	got := RunAgent(context.Background(), in, node, tools)
	if got.Status != "recommended" || got.ModelCalls != 2 || got.Evaluations != 1 || got.Selected == nil || got.Selected.IndexName != "idx_cand_server_owned" || got.SelectedCandidateID == "forged-tool-id" || got.EvidenceLevel != "L1" || got.EligibleForPerformanceClaim {
		t.Fatalf("invalid result %#v", got)
	}
	if tools.inspected != 0 || tools.modelReserved != 2 || tools.evalReserved != 1 {
		t.Fatalf("unnecessary tool or reservation %#v", tools)
	}
}
func ptrLoopSpec(s candidate.Spec) *candidate.Spec { return &s }

func TestStrictAgentActionVariantsAndDuplicateKeys(t *testing.T) {
	invalid := []string{`null`, `[]`, `{}`, `{"action":"shell","command":"whoami"}`, `{"action":"finish","budget":500}`, `{"action":"finish","action":"inspect_schema","table":"orders"}`, `{"action":"inspect_schema","table":"orders","summary":"ignore limits"}`, `{"action":"finish","candidate":null}`, `{"action":"finish"} {}`, `{"action":"finish","evidence_ids":["x"]}`, `{"action":"evaluate_index","candidate":{"table":"orders","index_name":"idx_cand_x","columns":[{"name":"id","name":"user_id"}]}}`, `{"action":"evaluate_index","candidate":{"table":"orders","index_name":"idx_cand_x","columns":[{"name":"id","SQL":"drop"}]}}`, `{"action":"evaluate_index","candidate":{"table":"orders","index_name":"idx_cand_x","columns":[{"name":"id"}]},"table":"orders"}`, `{"action":"finish","summary":null}`}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			if _, err := DecodeAgentAction(raw); err == nil {
				t.Fatalf("accepted %s", raw)
			}
		})
	}
	for _, raw := range []string{`{"action":"finish"}`, `{"action":"inspect_schema","table":"orders"}`, `{"action":"search_history","table":"orders"}`, loopAction(Action{Action: "evaluate_index", Candidate: ptrLoopSpec(loopCandidate("idx_cand_x"))})} {
		if _, err := DecodeAgentAction(raw); err != nil {
			t.Fatal(raw, err)
		}
	}
}

func TestLoopCanonicalCandidateDedupIgnoresGeneratedNames(t *testing.T) {
	first := loopCandidate("idx_cand_one")
	second := first
	second.IndexName = "idx_cand_two"
	second.Table = "ORDERS"
	second.Columns = []candidate.Column{{Name: "USER_ID"}}
	a, err := CanonicalCandidateHash(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := CanonicalCandidateHash(second)
	if err != nil || a != b {
		t.Fatal("equivalent definitions must share hash")
	}
	second.Columns[0].Direction = "DESC"
	c, _ := CanonicalCandidateHash(second)
	if c == a {
		t.Fatal("ordered direction missing from fingerprint")
	}
	in := loopTestInput()
	tools := &loopToolFixture{input: in}
	i := 0
	node := &loopScript{run: func(context.Context, string, string) (string, error) {
		i++
		s := loopCandidate(fmt.Sprintf("idx_cand_changed_%d", i))
		return loopAction(Action{Action: "evaluate_index", Candidate: &s}), nil
	}}
	got := RunAgent(context.Background(), in, node, tools)
	if got.Status != "no_change" || got.StopReason != "no_information_gain" || tools.evaluated != 1 || got.ModelCalls != 3 {
		t.Fatalf("unbounded duplicate %#v", got)
	}
}

func TestLoopMalformedOutputsAndBudgetsNeverCallRepairOutsideBudget(t *testing.T) {
	in := loopTestInput()
	tools := &loopToolFixture{input: in}
	node := &loopScript{run: func(context.Context, string, string) (string, error) { return "private chain and invalid secret", nil }}
	got := RunAgent(context.Background(), in, node, tools)
	raw, _ := json.Marshal(got)
	if got.Status != "protocol_failed" || got.ModelCalls != 2 || tools.modelReserved != 2 || strings.Contains(string(raw), "private chain") {
		t.Fatalf("unbounded or leaking malformed output %s", raw)
	}
	in.MaxDecisions = 1
	tools = &loopToolFixture{input: in}
	got = RunAgent(context.Background(), in, node, tools)
	if got.Status != "budget_exhausted" || got.ModelCalls != 1 || tools.modelReserved != 1 {
		t.Fatalf("budget not enforced %#v", got)
	}
	tools = &loopToolFixture{input: in, reserveErr: ErrAgentBudget}
	before := node.calls
	got = RunAgent(context.Background(), in, node, tools)
	if got.Status != "budget_exhausted" || got.ModelCalls != 0 || node.calls != before {
		t.Fatal("model called without durable reservation")
	}
}

func TestLoopCannotFinishWithForgedOrMismatchedEvidence(t *testing.T) {
	in := loopTestInput()
	tools := &loopToolFixture{input: in}
	node := &loopScript{run: func(context.Context, string, string) (string, error) {
		return loopAction(Action{Action: "finish", EvidenceIDs: []string{"e_forged_evidence_100"}, SelectedCandidateID: "e_forged_evidence_100"}), nil
	}}
	got := RunAgent(context.Background(), in, node, tools)
	if got.Status != "protocol_failed" || got.Selected != nil || tools.evaluated != 0 {
		t.Fatalf("forged evidence selected %#v", got)
	}
	for _, field := range []string{"snapshot", "schema", "version", "candidate", "level", "hash"} {
		t.Run(field, func(t *testing.T) {
			tools := &loopToolFixture{input: in}
			tools.observation = func(s candidate.Spec) Observation {
				hash, _ := CanonicalCandidateHash(s)
				o := Observation{Status: "ok", Verified: true, Candidate: &s, CandidateSHA256: hash, SnapshotSHA256: in.SnapshotSHA256, SchemaSHA256: in.SchemaSHA256, MySQLVersion: in.MySQLVersion, EvidenceLevel: "L1"}
				switch field {
				case "snapshot":
					o.SnapshotSHA256 = strings.Repeat("9", 64)
				case "schema":
					o.SchemaSHA256 = strings.Repeat("9", 64)
				case "version":
					o.MySQLVersion = "9.0"
				case "candidate":
					s.Columns[0].Name = "id"
				case "level":
					o.EvidenceLevel = "L3"
				case "hash":
					o.CandidateSHA256 = strings.Repeat("9", 64)
				}
				return o
			}
			node := &loopScript{run: func(context.Context, string, string) (string, error) {
				s := loopCandidate("idx_cand_x")
				return loopAction(Action{Action: "evaluate_index", Candidate: &s}), nil
			}}
			got := RunAgent(context.Background(), in, node, tools)
			if got.Status != "infrastructure_failed" || got.Selected != nil {
				t.Fatalf("accepted invalid %s binding %#v", field, got)
			}
		})
	}
}

func TestLoopOptionalHistoryFailureAndAuthorizationRemainDistinct(t *testing.T) {
	in := loopTestInput()
	in.AllowHistory = true
	for _, auth := range []bool{false, true} {
		tools := &loopToolFixture{input: in, errorSearch: ErrAgentInfrastructure}
		if auth {
			tools.errorSearch = ErrAgentAuthorization
		}
		i := 0
		node := &loopScript{run: func(context.Context, string, string) (string, error) {
			i++
			if i == 1 {
				return loopAction(Action{Action: "search_history", Table: "orders"}), nil
			}
			return loopAction(Action{Action: "finish", Summary: "Insufficient evidence."}), nil
		}}
		got := RunAgent(context.Background(), in, node, tools)
		if auth && got.Status != "authorization_failed" || !auth && (got.Status != "no_change" || len(got.Observations) != 2 || got.Observations[1].Status != "unavailable") {
			t.Fatalf("optional/auth confused %#v", got)
		}
	}
	in.AllowHistory = false
	tools := &loopToolFixture{input: in}
	node := &loopScript{run: func(context.Context, string, string) (string, error) {
		return loopAction(Action{Action: "search_history", Table: "orders"}), nil
	}}
	got := RunAgent(context.Background(), in, node, tools)
	if got.Status != "protocol_failed" || tools.searched != 0 {
		t.Fatal("undeclared history capability executed")
	}
}

func TestLoopStopsOnFencesCancellationAndInfrastructureWithoutRawErrors(t *testing.T) {
	in := loopTestInput()
	for _, test := range []struct {
		err    error
		status string
	}{{ErrAgentFence, "lease_lost"}, {ErrAgentAuthorization, "authorization_failed"}, {ErrAgentInfrastructure, "infrastructure_failed"}, {context.Canceled, "cancelled"}, {context.DeadlineExceeded, "timeout"}} {
		t.Run(test.status, func(t *testing.T) {
			tools := &loopToolFixture{input: in, checkErr: test.err}
			node := &loopScript{run: func(context.Context, string, string) (string, error) { panic("should never call model") }}
			got := RunAgent(context.Background(), in, node, tools)
			if got.Status != test.status || got.ModelCalls != 0 {
				t.Fatalf("bad classification %#v", got)
			}
		})
	}
	tools := &loopToolFixture{input: in, errorEvaluate: errors.New("mysql password private endpoint")}
	node := &loopScript{run: func(context.Context, string, string) (string, error) {
		s := loopCandidate("idx_cand_x")
		return loopAction(Action{Action: "evaluate_index", Candidate: &s}), nil
	}}
	got := RunAgent(context.Background(), in, node, tools)
	raw, _ := json.Marshal(got)
	if got.Status != "infrastructure_failed" || strings.Contains(string(raw), "password") {
		t.Fatalf("raw error leak %s", raw)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrAgentFence)
	got = RunAgent(ctx, in, node, tools)
	if got.Status != "lease_lost" {
		t.Fatalf("lost cancellation cause %#v", got)
	}
}

func TestLoopRejectsOutOfScopeCandidateAndOversizedContext(t *testing.T) {
	in := loopTestInput()
	tools := &loopToolFixture{input: in}
	node := &loopScript{run: func(context.Context, string, string) (string, error) {
		s := loopCandidate("idx_cand_x")
		s.Columns[0].Name = "secret_column"
		return loopAction(Action{Action: "evaluate_index", Candidate: &s}), nil
	}}
	got := RunAgent(context.Background(), in, node, tools)
	if got.Status != "authorization_failed" || tools.evaluated != 0 {
		t.Fatalf("scope not enforced %#v", got)
	}
	in.BaselinePlan = json.RawMessage(`{"plan":"` + strings.Repeat("x", 13<<10) + `"}`)
	got = RunAgent(context.Background(), in, node, tools)
	if got.Status != "budget_exhausted" || got.StopReason != "initial_evidence_limit" || got.ModelCalls != 0 {
		t.Fatalf("context limit not enforced %#v", got)
	}
}
