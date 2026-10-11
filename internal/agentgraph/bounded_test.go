package agentgraph

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBoundedGraphFeedbackExhaustionAndCancellation(t *testing.T) {
	validate := func(text string) error {
		if text == "valid" {
			return nil
		}
		return errors.New("private validator details")
	}
	node := &fixedNode{responses: []string{"malicious", "valid"}}
	got, err := DraftBounded(context.Background(), "untrusted evidence", node, validate, 3)
	if err != nil || !got.Accepted || got.Attempts != 2 || got.Prompt != "" || strings.Contains(node.prompts[1], "malicious") || strings.Contains(node.prompts[1], "private validator") {
		t.Fatalf("invalid decision: %+v %v", got, err)
	}
	node = &fixedNode{responses: []string{"bad", "bad", "bad", "valid"}}
	got, err = DraftBounded(context.Background(), "evidence", node, validate, 3)
	if err != nil || got.Accepted || got.Attempts != 3 || got.Text != "" || got.Reason != ReasonAttemptsExhausted || len(node.responses) != 1 {
		t.Fatalf("retry cap: %+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	node = &fixedNode{responses: []string{"valid"}}
	if _, err = DraftBounded(ctx, "evidence", node, validate, 3); err == nil || len(node.prompts) != 0 {
		t.Fatal("cancellation called provider")
	}
}
