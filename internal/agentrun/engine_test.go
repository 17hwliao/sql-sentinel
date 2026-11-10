package agentrun

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"sqlsentinel/internal/identity"
)

type scriptedModel struct {
	mu     sync.Mutex
	output []string
	calls  int
	err    error
}

func (m *scriptedModel) DraftWithSystem(_ context.Context, rules, prompt string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rules != Rules || prompt == "" {
		return "", ErrInvalid
	}
	m.calls++
	if m.err != nil {
		return "", m.err
	}
	if len(m.output) == 0 {
		return "", ErrInvalid
	}
	index := m.calls - 1
	if index >= len(m.output) {
		index = len(m.output) - 1
	}
	return m.output[index], nil
}

type fakeTasks struct {
	mu          sync.Mutex
	jobs        map[string]Job
	submitCalls int
	cancels     int
	unknown     bool
	entered     chan struct{}
	release     chan struct{}
	getErr      error
	cancelErr   error
}

func (f *fakeTasks) Submit(ctx context.Context, _ identity.Principal, id, query string) (Job, error) {
	if f.entered != nil {
		close(f.entered)
		select {
		case <-ctx.Done():
			return Job{}, ctx.Err()
		case <-f.release:
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submitCalls++
	if f.jobs == nil {
		f.jobs = map[string]Job{}
	}
	if old, ok := f.jobs[id]; ok {
		return old, nil
	}
	job := Job{ID: strings.Repeat("a", 32), DeliveryID: id, Status: "RUNNING"}
	f.jobs[id] = job
	if f.unknown {
		f.unknown = false
		return Job{}, ErrOutcomeUnknown
	}
	return job, nil
}
func (f *fakeTasks) Get(_ context.Context, _ identity.Principal, id string) (Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return Job{}, f.getErr
	}
	job, ok := f.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	return job, nil
}
func (f *fakeTasks) Cancel(_ context.Context, _ identity.Principal, id string) (Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancelErr != nil {
		return Job{}, f.cancelErr
	}
	job, ok := f.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	f.cancels++
	job.Status = "CANCELLED"
	f.jobs[id] = job
	return job, nil
}
func TestActionProtocol(t *testing.T) {
	for _, good := range []string{"submit", "status", "report", "finish"} {
		if got, err := action("{\"action\":\"" + good + "\"}"); err != nil || got != good {
			t.Fatalf("%s: %s %v", good, got, err)
		}
	}
	for _, bad := range []string{
		"{}", "[]", "null", "{\"action\":\"shell\"}", "{\"action\":\"submit\",\"url\":\"https://evil.test\"}",
		"{\"action\":\"submit\",\"action\":\"finish\"}", "{\"action\":1}", "{\"action\":\"finish\"} {}",
		"{\"action\":\"finish\",\"summary\":\"100x faster\"}", strings.Repeat("x", 2049),
	} {
		if _, err := action(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
func TestPromptBudget(t *testing.T) {
	if _, err := prompt(Run{SQL: strings.Repeat("x", 40<<10)}); !errors.Is(err, ErrBudget) {
		t.Fatal(err)
	}
	if _, err := prompt(Run{SQL: "SELECT id FROM orders LIMIT 1"}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckErrorClassification(t *testing.T) {
	if checkError(nil) != nil {
		t.Fatal("nil changed")
	}
	if !errors.Is(checkError(ErrForbidden), ErrForbidden) {
		t.Fatal("revocation changed")
	}
	for _, err := range []error{ErrUnavailable, context.DeadlineExceeded, errors.New("private driver error")} {
		if !errors.Is(checkError(err), ErrUnavailable) {
			t.Fatal("outage became forbidden")
		}
	}
}
func TestJobEvidenceBoundary(t *testing.T) {
	r := Run{ID: strings.Repeat("a", 32)}
	good := Job{ID: strings.Repeat("b", 32), DeliveryID: delivery(r), Status: "COMPLETED", Evidence: &Evidence{Level: "L1", Summary: "计划级证据", Outcome: "no_change"}}
	if err := validateJob(good, r); err != nil {
		t.Fatal(err)
	}
	wrong := good
	wrong.DeliveryID = "other"
	if validateJob(wrong, r) == nil {
		t.Fatal("foreign job")
	}
	wrong = good
	copy := *good.Evidence
	wrong.Evidence = &copy
	wrong.Evidence.Level = "L3"
	if validateJob(wrong, r) == nil {
		t.Fatal("upgrade")
	}
	wrong.Evidence.Level = "L1"
	wrong.Evidence.Summary = strings.Repeat("x", 2049)
	if validateJob(wrong, r) == nil {
		t.Fatal("oversize")
	}
}
func TestConfigurationAndInputHash(t *testing.T) {
	p := identity.Principal{Subject: "member", Tenant: "tenant", Project: "project", Tables: []string{"orders"}}
	cfg := Config{Version: "v1", DefaultModel: "qwen3.8/test:model", ModelAliases: []string{"qwen3.8/test:model"}, ToolsEnabled: true}
	model := &scriptedModel{}
	svc, err := New(&sql.DB{}, cfg, map[string]Model{cfg.DefaultModel: model}, &fakeTasks{}, func(context.Context, identity.Principal, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	got := svc.Configuration(p)
	if got.MaxModelCalls != 4 || got.MaxJobs != 1 || got.InnerModelCalls != 5 || got.InnerEvaluations != 2 || got.RunSeconds != 180 {
		t.Fatalf("%+v", got)
	}
	got.Tables[0] = "changed"
	if p.Tables[0] != "orders" {
		t.Fatal("mutable scope")
	}
	cfg.ModelAliases[0] = "changed"
	if svc.Configuration(p).ModelAliases[0] != cfg.DefaultModel {
		t.Fatal("mutable config")
	}
	if inputDigest(Input{RequestID: "x", SQL: "one"}) == inputDigest(Input{RequestID: "x", SQL: "two"}) {
		t.Fatal("input hash collision")
	}
	if _, _, err = svc.Create(context.Background(), p, Input{RequestID: "x"}); !errors.Is(err, ErrSQLRequired) {
		t.Fatal(err)
	}
}
