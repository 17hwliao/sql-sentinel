package agentgraph

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"sqlsentinel/internal/candidate"
	"sqlsentinel/internal/shadowdata"
)

// These are independently scripted environment scenarios. They exercise the
// public loop contract, not the implementation's private parsing helpers. No
// network, database, billing or paid model endpoint is used by this file.
type scenarioView struct {
	Task         LoopInput     `json:"task"`
	Observations []Observation `json:"observations"`
	Feedback     string        `json:"feedback"`
}

type scenarioNode struct {
	calls  int
	rules  []string
	decide func(context.Context, scenarioView, int) (string, error)
}

func (n *scenarioNode) Draft(ctx context.Context, prompt string) (string, error) {
	return n.DraftWithSystem(ctx, "", prompt)
}

func (n *scenarioNode) DraftWithSystem(ctx context.Context, rules, prompt string) (string, error) {
	n.calls++
	n.rules = append(n.rules, rules)
	var view scenarioView
	if err := json.Unmarshal([]byte(prompt), &view); err != nil {
		return "", fmt.Errorf("scenario expected a JSON observation envelope: %w", err)
	}
	return n.decide(ctx, view, n.calls)
}

type scenarioTools struct {
	in                                         LoopInput
	models, evaluations, inspections, searches int
	maxModels, maxEvaluations                  int
	checkErr                                   error
	onEvaluate                                 func(candidate.Spec, int) (Observation, error)
	onSearch                                   func(string) (Observation, error)
}

func (s *scenarioTools) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.checkErr
}
func (s *scenarioTools) Reserve(ctx context.Context, kind string) error {
	if err := s.Check(ctx); err != nil {
		return err
	}
	switch kind {
	case "model":
		if s.maxModels > 0 && s.models >= s.maxModels {
			return ErrAgentBudget
		}
		s.models++
	case "evaluation":
		if s.maxEvaluations > 0 && s.evaluations >= s.maxEvaluations {
			return ErrAgentBudget
		}
		s.evaluations++
	default:
		return ErrAgentInfrastructure
	}
	return nil
}
func (s *scenarioTools) Inspect(ctx context.Context, table string) (Observation, error) {
	s.inspections++
	return Observation{Status: "ok", Facts: json.RawMessage(`{"schema_reused":true}`)}, s.Check(ctx)
}
func (s *scenarioTools) Search(ctx context.Context, table string) (Observation, error) {
	s.searches++
	if err := s.Check(ctx); err != nil {
		return Observation{}, err
	}
	if s.onSearch != nil {
		return s.onSearch(table)
	}
	return Observation{Status: "unavailable", Code: "history_unavailable"}, nil
}
func (s *scenarioTools) Evaluate(ctx context.Context, spec candidate.Spec) (Observation, error) {
	if err := s.Check(ctx); err != nil {
		return Observation{}, err
	}
	if s.onEvaluate != nil {
		return s.onEvaluate(spec, s.evaluations)
	}
	return s.verified(spec), nil
}
func (s *scenarioTools) verified(spec candidate.Spec) Observation {
	hash, err := CanonicalCandidateHash(spec)
	if err != nil {
		panic(err)
	}
	return Observation{Status: "ok", Candidate: &spec, CandidateSHA256: hash,
		SnapshotSHA256: s.in.SnapshotSHA256, SchemaSHA256: s.in.SchemaSHA256,
		MySQLVersion: s.in.MySQLVersion, EvidenceLevel: "L1", Verified: true,
		Facts: json.RawMessage(`{"candidate_index_used":true,"estimated_rows":20,"measured":false}`)}
}

func scenarioInput() LoopInput {
	query := "SELECT id FROM orders WHERE user_id=42 AND status='paid'"
	digest := sha256.Sum256([]byte(query))
	return LoopInput{JobID: strings.Repeat("a", 32), Attempt: 1, SQL: query,
		SQLSHA256: hex.EncodeToString(digest[:]), Tables: []string{"orders"},
		Schema:         []shadowdata.Table{{Name: "orders", Columns: []shadowdata.Column{{Name: "id", Type: "bigint", Nullable: "NO"}, {Name: "user_id", Type: "bigint", Nullable: "NO"}, {Name: "status", Type: "varchar(16)", Nullable: "NO"}}}},
		BaselinePlan:   json.RawMessage(`{"query_block":{"table":{"table_name":"orders","access_type":"ALL","rows_examined_per_scan":10000}}}`),
		SnapshotSHA256: strings.Repeat("c", 64), SchemaSHA256: strings.Repeat("d", 64), MySQLVersion: "8.4.11", MaxDecisions: 5, MaxEvaluations: 2}
}
func scenarioCandidate(name, column string) candidate.Spec {
	return candidate.Spec{Table: "orders", IndexName: name, Columns: []candidate.Column{{Name: column, Direction: "ASC"}}}
}
func scenarioJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
func scenarioFinish(view scenarioView, selectVerified bool) string {
	a := Action{Action: "finish", Summary: "No measured performance claim. Current plan evidence only."}
	if len(view.Observations) > 0 {
		a.EvidenceIDs = []string{view.Observations[0].ID}
	}
	if selectVerified {
		for i := len(view.Observations) - 1; i >= 0; i-- {
			o := view.Observations[i]
			if o.Action == "evaluate_index" && o.Verified {
				a.SelectedCandidateID = o.ID
				a.EvidenceIDs = append(a.EvidenceIDs, o.ID)
				break
			}
		}
	}
	return scenarioJSON(a)
}

func TestLoopScenarioUsesActualRejectedObservationToAdjust(t *testing.T) {
	in := scenarioInput()
	tools := &scenarioTools{in: in}
	tools.onEvaluate = func(spec candidate.Spec, n int) (Observation, error) {
		if spec.Columns[0].Name == "status" {
			return Observation{Status: "rejected", Code: "candidate_rejected", Facts: json.RawMessage(`{"admitted":false,"measured":false}`)}, nil
		}
		return tools.verified(spec), nil
	}
	sawFailure := false
	node := &scenarioNode{decide: func(_ context.Context, v scenarioView, n int) (string, error) {
		for _, o := range v.Observations {
			if o.Action == "evaluate_index" && o.Status == "rejected" {
				sawFailure = true
			}
		}
		for _, o := range v.Observations {
			if o.Action == "evaluate_index" && o.Verified {
				return scenarioFinish(v, true), nil
			}
		}
		column := "status"
		if sawFailure {
			column = "user_id"
		}
		spec := scenarioCandidate("idx_cand_scenario", column)
		return scenarioJSON(Action{Action: "evaluate_index", Candidate: &spec}), nil
	}}
	got := RunAgent(context.Background(), in, node, tools)
	if got.Status != "recommended" || !sawFailure || got.Selected == nil || got.Selected.Columns[0].Name != "user_id" || tools.evaluations != 2 || node.calls != 3 {
		t.Fatalf("closed loop not observed: status=%s selected=%v calls=%d evaluations=%d sawFailure=%v", got.Status, got.Selected, node.calls, tools.evaluations, sawFailure)
	}
	if got.EvidenceLevel != "L1" || got.EligibleForPerformanceClaim {
		t.Fatal("plan verification was promoted to a performance claim")
	}
	for _, rules := range node.rules {
		if rules == "" {
			t.Fatal("trusted rules were not passed separately")
		}
	}
}

func TestLoopScenarioFirstCandidateAndNoCandidateAreDistinctSuccesses(t *testing.T) {
	for _, recommend := range []bool{true, false} {
		t.Run(fmt.Sprint(recommend), func(t *testing.T) {
			in := scenarioInput()
			tools := &scenarioTools{in: in}
			node := &scenarioNode{decide: func(_ context.Context, v scenarioView, n int) (string, error) {
				if recommend && n == 1 {
					spec := scenarioCandidate("idx_cand_first", "user_id")
					return scenarioJSON(Action{Action: "evaluate_index", Candidate: &spec}), nil
				}
				return scenarioFinish(v, recommend), nil
			}}
			got := RunAgent(context.Background(), in, node, tools)
			if recommend {
				if got.Status != "recommended" || got.Selected == nil || tools.evaluations != 1 || node.calls != 2 {
					t.Fatalf("recommended path: %+v", got)
				}
			} else if got.Status != "no_change" || got.Selected != nil || tools.evaluations != 0 || node.calls != 1 {
				t.Fatalf("legitimate no-candidate result: %+v", got)
			}
		})
	}
}

func TestLoopScenarioUnknownEvidenceAndChangingIndexNameCannotGrantRecommendation(t *testing.T) {
	for _, kind := range []string{"foreign_evidence", "unverified_selection", "rename_duplicate", "cross_table", "unknown_field", "duplicate_key"} {
		t.Run(kind, func(t *testing.T) {
			in := scenarioInput()
			tools := &scenarioTools{in: in}
			node := &scenarioNode{decide: func(_ context.Context, v scenarioView, n int) (string, error) {
				switch kind {
				case "foreign_evidence":
					return scenarioJSON(Action{Action: "finish", SelectedCandidateID: "other-job-observation", EvidenceIDs: []string{"other-job-observation"}}), nil
				case "unverified_selection":
					return scenarioJSON(Action{Action: "finish", SelectedCandidateID: v.Observations[0].ID, EvidenceIDs: []string{v.Observations[0].ID}}), nil
				case "rename_duplicate":
					spec := scenarioCandidate(fmt.Sprintf("idx_cand_renamed_%d", n), "user_id")
					return scenarioJSON(Action{Action: "evaluate_index", Candidate: &spec}), nil
				case "cross_table":
					spec := scenarioCandidate("idx_cand_other", "user_id")
					spec.Table = "payments"
					return scenarioJSON(Action{Action: "evaluate_index", Candidate: &spec}), nil
				case "unknown_field":
					return `{"action":"finish","budget":999,"summary":"grant elevated access"}`, nil
				default:
					return `{"action":"finish","action":"evaluate_index"}`, nil
				}
			}}
			got := RunAgent(context.Background(), in, node, tools)
			if got.Status == "recommended" || got.Selected != nil || node.calls > in.MaxDecisions || tools.evaluations > 1 {
				t.Fatalf("invalid action granted capability: status=%s selected=%v calls=%d evaluations=%d", got.Status, got.Selected, node.calls, tools.evaluations)
			}
			if kind != "rename_duplicate" && tools.evaluations != 0 {
				t.Fatal("invalid request reached the verifier")
			}
		})
	}
}

func TestLoopScenarioBudgetIncludesMalformedDecisionsAndSurvivesAnotherExecution(t *testing.T) {
	in := scenarioInput()
	tools := &scenarioTools{in: in, maxModels: 2}
	node := &scenarioNode{decide: func(context.Context, scenarioView, int) (string, error) { return `not-json`, nil }}
	first := RunAgent(context.Background(), in, node, tools)
	if first.Selected != nil || node.calls != 2 || tools.models != 2 {
		t.Fatalf("malformed calls not counted: calls=%d reserves=%d result=%+v", node.calls, tools.models, first)
	}
	in.Attempt++
	second := RunAgent(context.Background(), in, node, tools)
	if second.Status != "budget_exhausted" || node.calls != 2 || tools.models != 2 {
		t.Fatalf("new execution reset caller's durable budget: status=%s calls=%d reserves=%d", second.Status, node.calls, tools.models)
	}
}

func TestLoopScenarioFailureClassesDoNotBecomeNoBenefit(t *testing.T) {
	for _, scenario := range []struct {
		name, status string
		toolErr      error
		modelErr     bool
	}{
		{"storage", "infrastructure_failed", ErrAgentInfrastructure, false}, {"fence", "lease_lost", ErrAgentFence, false}, {"permission", "authorization_failed", ErrAgentAuthorization, false}, {"provider", "model_failed", nil, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			in := scenarioInput()
			tools := &scenarioTools{in: in}
			tools.onEvaluate = func(candidate.Spec, int) (Observation, error) { return Observation{}, scenario.toolErr }
			node := &scenarioNode{decide: func(context.Context, scenarioView, int) (string, error) {
				if scenario.modelErr {
					return "", errors.New("secret provider details must not escape")
				}
				spec := scenarioCandidate("idx_cand_fail", "user_id")
				return scenarioJSON(Action{Action: "evaluate_index", Candidate: &spec}), nil
			}}
			got := RunAgent(context.Background(), in, node, tools)
			if got.Status != scenario.status || got.Selected != nil {
				t.Fatalf("failure class changed: status=%s expected=%s", got.Status, scenario.status)
			}
			raw, _ := json.Marshal(got)
			if bytes.Contains(raw, []byte("secret provider")) {
				t.Fatal("provider details escaped into a public result")
			}
		})
	}
}

func TestLoopScenarioCancellationTimeoutAndOptionalHistory(t *testing.T) {
	t.Run("cancelled before dispatch", func(t *testing.T) {
		in := scenarioInput()
		tools := &scenarioTools{in: in}
		node := &scenarioNode{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got := RunAgent(ctx, in, node, tools)
		if got.Status != "cancelled" || node.calls != 0 || tools.models != 0 {
			t.Fatalf("cancel invoked provider: %+v", got)
		}
	})
	t.Run("deadline while model waits", func(t *testing.T) {
		in := scenarioInput()
		tools := &scenarioTools{in: in}
		node := &scenarioNode{decide: func(ctx context.Context, _ scenarioView, _ int) (string, error) { <-ctx.Done(); return "", ctx.Err() }}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		got := RunAgent(ctx, in, node, tools)
		if got.Status != "timeout" || node.calls != 1 || tools.models != 1 {
			t.Fatalf("timeout misclassified: %+v", got)
		}
	})
	t.Run("absent history cannot be called", func(t *testing.T) {
		in := scenarioInput()
		tools := &scenarioTools{in: in}
		node := &scenarioNode{decide: func(_ context.Context, v scenarioView, n int) (string, error) {
			if n == 1 {
				return scenarioJSON(Action{Action: "search_history", Table: "orders"}), nil
			}
			return scenarioFinish(v, false), nil
		}}
		got := RunAgent(context.Background(), in, node, tools)
		if got.Status != "no_change" || tools.searches != 0 || tools.evaluations != 0 {
			t.Fatalf("unconfigured optional dependency used: %+v searches=%d", got, tools.searches)
		}
	})
	t.Run("configured history outage is not a diagnostic outage", func(t *testing.T) {
		in := scenarioInput()
		in.AllowHistory = true
		tools := &scenarioTools{in: in}
		node := &scenarioNode{decide: func(_ context.Context, v scenarioView, n int) (string, error) {
			if n == 1 {
				return scenarioJSON(Action{Action: "search_history", Table: "orders"}), nil
			}
			return scenarioFinish(v, false), nil
		}}
		got := RunAgent(context.Background(), in, node, tools)
		if got.Status != "no_change" || tools.searches != 1 {
			t.Fatalf("optional outage blocked unrelated work: %+v searches=%d", got, tools.searches)
		}
	})
	t.Run("history authentication failure is fatal", func(t *testing.T) {
		in := scenarioInput()
		in.AllowHistory = true
		tools := &scenarioTools{in: in, onSearch: func(string) (Observation, error) { return Observation{}, ErrAgentAuthorization }}
		node := &scenarioNode{decide: func(context.Context, scenarioView, int) (string, error) {
			return scenarioJSON(Action{Action: "search_history", Table: "orders"}), nil
		}}
		got := RunAgent(context.Background(), in, node, tools)
		if got.Status != "authorization_failed" || node.calls != 1 {
			t.Fatalf("history denial hidden as empty results: %+v", got)
		}
	})
}

func TestLoopScenarioSnapshotOrVersionChangeInvalidatesEarlierEvidence(t *testing.T) {
	for _, changed := range []string{"snapshot", "schema", "version", "candidate_hash"} {
		t.Run(changed, func(t *testing.T) {
			in := scenarioInput()
			tools := &scenarioTools{in: in}
			tools.onEvaluate = func(spec candidate.Spec, _ int) (Observation, error) {
				o := tools.verified(spec)
				switch changed {
				case "snapshot":
					o.SnapshotSHA256 = strings.Repeat("e", 64)
				case "schema":
					o.SchemaSHA256 = strings.Repeat("f", 64)
				case "version":
					o.MySQLVersion = "different"
				default:
					o.CandidateSHA256 = strings.Repeat("0", 64)
				}
				return o, nil
			}
			node := &scenarioNode{decide: func(context.Context, scenarioView, int) (string, error) {
				spec := scenarioCandidate("idx_cand_stale", "user_id")
				return scenarioJSON(Action{Action: "evaluate_index", Candidate: &spec}), nil
			}}
			got := RunAgent(context.Background(), in, node, tools)
			if got.Status != "infrastructure_failed" || got.Selected != nil || tools.evaluations != 1 {
				t.Fatalf("stale evidence accepted: %+v", got)
			}
		})
	}
}

// A/B is deliberately a local orchestration comparison, not an optimization
// benchmark. The same deterministic scenario policy and a three-model-call cap
// are used. The old path validates one admitted proposal after drafting; it has
// no channel through which the verifier can ask for a different candidate.
func TestLoopScenarioFixedFlowComparison(t *testing.T) {
	t.Log("case,flow,model_calls,verification_calls,outcome,wall_ms,cost_scope")
	for _, adjust := range []bool{false, true} {
		t.Run(fmt.Sprint(adjust), func(t *testing.T) {
			in := scenarioInput()
			in.MaxDecisions = 3
			column := "user_id"
			if adjust {
				column = "status"
			}
			first := scenarioCandidate("idx_cand_compare", column)
			baselineCalls := 0
			baselineNode := scenarioRawNode(func(context.Context, string) (string, error) { baselineCalls++; return scenarioJSON(first), nil })
			start := time.Now()
			draft, err := DraftBounded(context.Background(), "same read-only SQL and schema scenario", baselineNode, func(text string) error {
				spec, e := candidate.DecodeStrict(strings.NewReader(text))
				if e != nil {
					return e
				}
				return candidate.Validate(spec)
			}, 3)
			if err != nil || !draft.Accepted {
				t.Fatalf("baseline proposal failed: %v", err)
			}
			makeTools := func() *scenarioTools {
				tools := &scenarioTools{in: in}
				tools.onEvaluate = func(spec candidate.Spec, _ int) (Observation, error) {
					if adjust && spec.Columns[0].Name == "status" {
						return Observation{Status: "rejected", Code: "candidate_rejected", Facts: json.RawMessage(`{"admitted":false}`)}, nil
					}
					return tools.verified(spec), nil
				}
				return tools
			}
			baselineTools := makeTools()
			admitted, err := candidate.DecodeStrict(strings.NewReader(draft.Text))
			if err != nil {
				t.Fatal(err)
			}
			if err = baselineTools.Reserve(context.Background(), "evaluation"); err != nil {
				t.Fatal(err)
			}
			observation, err := baselineTools.Evaluate(context.Background(), admitted)
			if err != nil {
				t.Fatal(err)
			}
			baselineOutcome := "verified_candidate"
			if observation.Status == "rejected" {
				baselineOutcome = "verification_rejected_no_feedback"
			}
			t.Logf("adjust=%t,fixed,%d,%d,%s,%.3f,fixture_only_no_paid_calls", adjust, baselineCalls, baselineTools.evaluations, baselineOutcome, float64(time.Since(start))/float64(time.Millisecond))
			tools := makeTools()
			node := &scenarioNode{decide: func(_ context.Context, v scenarioView, n int) (string, error) {
				for _, o := range v.Observations {
					if o.Action == "evaluate_index" && o.Verified {
						return scenarioFinish(v, true), nil
					}
				}
				spec := first
				for _, o := range v.Observations {
					if o.Action == "evaluate_index" && o.Status == "rejected" {
						spec = scenarioCandidate("idx_cand_adjusted", "user_id")
					}
				}
				return scenarioJSON(Action{Action: "evaluate_index", Candidate: &spec}), nil
			}}
			start = time.Now()
			got := RunAgent(context.Background(), in, node, tools)
			wall := time.Since(start)
			if got.Status != "recommended" || got.Selected == nil || node.calls > 3 {
				t.Fatalf("agent comparison did not finish within common model cap: %+v", got)
			}
			expectedCalls, expectedEvaluations := 2, 1
			if adjust {
				expectedCalls, expectedEvaluations = 3, 2
			}
			if node.calls != expectedCalls || tools.evaluations != expectedEvaluations {
				t.Fatalf("unexpected actual counts: calls=%d evaluations=%d", node.calls, tools.evaluations)
			}
			t.Logf("adjust=%t,agent,%d,%d,%s,%.3f,fixture_only_no_paid_calls", adjust, node.calls, tools.evaluations, got.Status, float64(wall)/float64(time.Millisecond))
		})
	}
}

type scenarioRawNode func(context.Context, string) (string, error)

func (f scenarioRawNode) Draft(ctx context.Context, prompt string) (string, error) {
	return f(ctx, prompt)
}
