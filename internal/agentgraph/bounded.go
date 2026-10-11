package agentgraph

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/compose"
	"strings"
)

// DraftResult records the bounded proposal decision. Accepted still means L1:
// deterministic shadow verification and human approval remain mandatory.
type DraftResult struct {
	Prompt   string
	Text     string
	Attempts int
	Accepted bool
	Reason   string
}

// DraftBounded uses explicit evidence, draft, validation and downgrade nodes.
// Validation errors feed only a fixed corrective instruction back to the model;
// raw database errors or model output are never reflected into the next prompt.
func DraftBounded(ctx context.Context, prompt string, node DraftNode, validate func(string) error, limit int) (DraftResult, error) {
	if node == nil || validate == nil || limit < 1 || limit > 3 {
		return DraftResult{}, errors.New("invalid_draft_graph")
	}
	graph := compose.NewGraph[DraftResult, DraftResult]()
	steps := []struct {
		name string
		run  func(context.Context, DraftResult) (DraftResult, error)
	}{
		{"evidence", func(ctx context.Context, s DraftResult) (DraftResult, error) {
			if err := ctx.Err(); err != nil {
				return s, err
			}
			if len(s.Prompt) > 16384 || strings.TrimSpace(s.Prompt) == "" {
				return s, errors.New("draft_evidence_out_of_bounds")
			}
			return s, nil
		}},
		{"draft", func(ctx context.Context, s DraftResult) (DraftResult, error) {
			if err := ctx.Err(); err != nil {
				return s, err
			}
			s.Attempts++
			text, err := node.Draft(ctx, s.Prompt)
			if err != nil {
				s.Reason = ReasonModelNodeFailed
				s.Text = ""
				return s, nil
			}
			s.Text = text
			return s, nil
		}},
		{"validate", func(ctx context.Context, s DraftResult) (DraftResult, error) {
			if err := ctx.Err(); err != nil {
				return s, err
			}
			s.Accepted = s.Reason == "" && len(s.Text) <= 65536 && validate(s.Text) == nil
			if !s.Accepted && s.Reason == "" {
				s.Prompt += "\nPrevious proposal failed deterministic admission. Return only the constrained CandidateSpec JSON using the supplied schema."
			}
			return s, nil
		}},
		{"decision", func(_ context.Context, s DraftResult) (DraftResult, error) {
			s.Prompt = ""
			if !s.Accepted {
				s.Text = ""
				if s.Reason == "" {
					s.Reason = ReasonAttemptsExhausted
				}
			}
			return s, nil
		}},
	}
	for _, step := range steps {
		if err := graph.AddLambdaNode(step.name, compose.InvokableLambda(step.run)); err != nil {
			return DraftResult{}, err
		}
	}
	for _, edge := range [][2]string{{compose.START, "evidence"}, {"evidence", "draft"}, {"draft", "validate"}, {"decision", compose.END}} {
		if err := graph.AddEdge(edge[0], edge[1]); err != nil {
			return DraftResult{}, err
		}
	}
	if err := graph.AddBranch("validate", compose.NewGraphBranch(func(_ context.Context, s DraftResult) (string, error) {
		if !s.Accepted && s.Reason == "" && s.Attempts < limit {
			return "draft", nil
		}
		return "decision", nil
	}, map[string]bool{"draft": true, "decision": true})); err != nil {
		return DraftResult{}, err
	}
	runnable, err := graph.Compile(ctx, compose.WithMaxRunSteps(12))
	if err != nil {
		return DraftResult{}, err
	}
	return runnable.Invoke(ctx, DraftResult{Prompt: prompt})
}
