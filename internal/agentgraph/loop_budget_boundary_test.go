package agentgraph

import (
	"context"
	"strings"
	"testing"
)

func TestInitialEvidenceLimitStopsWithoutDispatchOrInfrastructureRetry(t *testing.T) {
	in := loopTestInput()
	// information_schema can legitimately contain a large ENUM definition. It
	// is a context limit, not a broken database or permission failure.
	in.Schema[0].Columns[0].Type = "enum('" + strings.Repeat("bounded_enum_value','", 1000) + "last')"
	tools := &loopToolFixture{input: in}
	model := &loopScript{run: func(context.Context, string, string) (string, error) {
		return `{"action":"finish","evidence_ids":[]}`, nil
	}}
	result := RunAgent(context.Background(), in, model, tools)
	if result.Status != "budget_exhausted" || result.StopReason != "initial_evidence_limit" || model.calls != 0 || tools.modelReserved != 0 || tools.evalReserved != 0 {
		t.Fatalf("initial context limit must stop safely without dispatch; got status=%s reason=%s calls=%d", result.Status, result.StopReason, model.calls)
	}
}
