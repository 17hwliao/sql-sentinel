package agentgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDraftSystemPolicySeparatedFromUntrustedData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Messages []struct{ Role, Content string }
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.Messages) != 2 || in.Messages[0].Role != "system" || in.Messages[0].Content != "policy" || in.Messages[1].Role != "user" || in.Messages[1].Content != "untrusted evidence" {
			t.Error("system policy promoted from user data")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	node, e := NewOpenAINode(context.Background(), "protocol-fixture", "fixture", server.URL+"/v1")
	if e != nil {
		t.Fatal(e)
	}
	output, e := node.(interface {
		DraftWithSystem(context.Context, string, string) (string, error)
	}).DraftWithSystem(context.Background(), "policy", "untrusted evidence")
	if e != nil || output != "answer" {
		t.Fatalf("adapter: %q %v", output, e)
	}
}
