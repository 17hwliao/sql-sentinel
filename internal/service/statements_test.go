package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sqlsentinel/internal/agentgraph"
	"strings"
	"testing"
)

func TestChangedWhereUsesCompleteHeadStatement(t *testing.T) {
	head := "SELECT id\nFROM orders\nWHERE user_id = 42;\n\nSELECT status FROM orders WHERE note='a;b';"
	statements, err := Statements(head)
	if err != nil || len(statements) != 2 {
		t.Fatalf("%v %+v", err, statements)
	}
	changed := ChangedLines("@@ -1,3 +1,3 @@\n SELECT id\n FROM orders\n-WHERE user_id = 1;\n+WHERE user_id = 42;")
	if !changed[3] || changed[1] || statements[0].SQL != "SELECT id\nFROM orders\nWHERE user_id = 42;" || statements[0].StartLine != 1 || statements[0].EndLine != 3 {
		t.Fatalf("%+v %+v", statements, changed)
	}
}
func TestSQLFileBoundariesIgnoreLiteralComments(t *testing.T) {
	statements, err := Statements("SELECT 'x;''y' FROM orders; /* ; */ SELECT `id` FROM orders -- ;\n;")
	if err != nil || len(statements) != 2 {
		t.Fatal(err, statements)
	}
	if _, err = Statements("SELECT 'broken"); err == nil {
		t.Fatal("unterminated accepted")
	}
}
func TestNativeWebhookRejectsInvalidSignatureBeforeNetwork(t *testing.T) {
	g := &GitHub{Secret: strings.Repeat("s", 32), Client: &http.Client{Transport: forbiddenTransport{t: t}}}
	w := httptest.NewRecorder()
	g.Receive(w, httptest.NewRequest("POST", "/webhooks/github", strings.NewReader(`{}`)), nil)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

type forbiddenTransport struct{ t *testing.T }

func (f forbiddenTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Fatal("invalid webhook made network request")
	return nil, fmt.Errorf("forbidden")
}
func TestEinoModelStreamsThroughGatewayContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("body")
		}
		if body["stream"] != true {
			t.Fatal("nonstream request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"{}\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	node, err := agentgraph.NewOpenAINode(context.Background(), "test-key", "model", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := agentgraph.NewEinoGraph(node)
	if err != nil {
		t.Fatal(err)
	}
	result, err := graph.Draft(context.Background(), "test")
	if err != nil || result != "{}" {
		t.Fatalf("%q %v", result, err)
	}
}
