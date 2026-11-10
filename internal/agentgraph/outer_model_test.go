package agentgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOuterModelBoundIsIndependentOfPrompt(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			MaxTokens int `json:"max_tokens"`
			Messages  []struct{ Role, Content string }
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.MaxTokens != 768 || len(in.Messages) != 2 || in.Messages[0].Role != "system" || in.Messages[0].Content != "outer policy" {
			t.Error("outer adapter lost output bound or role separation")
		}
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"{}\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	node, err := NewBoundedOpenAINode(context.Background(), "fixture-not-a-real-key", "fixture", server.URL+"/v1", 768)
	if err != nil {
		t.Fatal(err)
	}
	system := node.(interface {
		DraftWithSystem(context.Context, string, string) (string, error)
	})
	if _, err = system.DraftWithSystem(context.Background(), "outer policy", "untrusted SQL"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("unexpected dispatch count %d", calls)
	}
}
