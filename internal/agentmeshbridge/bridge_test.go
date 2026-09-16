package agentmeshbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunConsumesSSEWithoutPersistingKeyOrDelta(t *testing.T) {
	const secret = "bridge-key-secret"
	const delta = "MODEL_DELTA_MUST_NOT_PERSIST"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "bridge-model" || !request.Stream || len(request.Messages) != 1 || request.Messages[0].Content != ConnectivityPrompt {
			t.Fatalf("unexpected request: %#v", request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-AgentMesh-Trace-ID", "trace-016")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"" + delta + "\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	report := Run(context.Background(), server.Client(), Config{BaseURL: server.URL, APIKey: secret, Model: "bridge-model", AgentMeshCommitSHA: "e81b658", Timeout: time.Second})
	if report.Status != StatusCompleted || report.Code != "success" || !report.SSECompleted || report.TraceID != "trace-016" {
		t.Fatalf("report = %#v", report)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), delta) || strings.Contains(string(raw), ConnectivityPrompt) {
		t.Fatalf("unsafe report: %s", raw)
	}
}

func TestRunStableRejections(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		content string
		body    string
		want    string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: ReasonAuthenticationFailed},
		{name: "limited", status: http.StatusTooManyRequests, want: ReasonRateLimited},
		{name: "not sse", status: http.StatusOK, content: "application/json", want: ReasonResponseNotSSE},
		{name: "missing trace", status: http.StatusOK, content: "text/event-stream", body: "data: [DONE]\n\n", want: ReasonTraceMissing},
		{name: "stream error", status: http.StatusOK, content: "text/event-stream", body: "data: {\"error\":{\"code\":\"stream_interrupted\"}}\n\n", want: ReasonStreamInterrupted},
		{name: "incomplete", status: http.StatusOK, content: "text/event-stream", body: "data: {\"choices\":[]}\n\n", want: ReasonStreamIncomplete},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", test.content)
				if test.status == http.StatusOK && test.content == "text/event-stream" && test.name != "missing trace" {
					w.Header().Set("X-AgentMesh-Trace-ID", "trace")
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			report := Run(context.Background(), server.Client(), Config{BaseURL: server.URL, APIKey: "key", Model: "model", AgentMeshCommitSHA: "e81b658", Timeout: time.Second})
			if report.Code != test.want || report.Status == StatusCompleted || report.EvidenceLevel != EvidenceLevel || report.EligibleForPerformanceClaim {
				t.Fatalf("report = %#v, want code %q", report, test.want)
			}
		})
	}
}

func TestDraftCandidateAccumulatesOnlySSEDeltaText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !request.Stream || request.Messages[0].Content != "evidence prompt" {
			t.Fatalf("request=%#v", request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"table\\\":\\\"orders\\\",\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"\\\"index_name\\\":\\\"idx_cand_x\\\",\\\"columns\\\":[]}\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	text, err := DraftCandidate(context.Background(), server.Client(), Config{BaseURL: server.URL, APIKey: "key", Model: "model", AgentMeshCommitSHA: "local", Timeout: time.Second}, "evidence prompt")
	if err != nil || text != `{"table":"orders","index_name":"idx_cand_x","columns":[]}` {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

func TestDraftCandidateRejectsErrorEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"error\":{\"code\":\"stream_interrupted\"}}\n\n"))
	}))
	defer server.Close()
	_, err := DraftCandidate(context.Background(), server.Client(), Config{BaseURL: server.URL, APIKey: "key", Model: "model", AgentMeshCommitSHA: "local", Timeout: time.Second}, "evidence prompt")
	if err == nil {
		t.Fatal("error event was accepted")
	}
}

func TestLoadConfigRejectsBeforeRequest(t *testing.T) {
	lookup := func(name string) string {
		if name == "AGENTMESH_BASE_URL" {
			return "http://localhost:8080"
		}
		return ""
	}
	cfg, report := LoadConfig(lookup)
	if cfg != (Config{}) || report.Status != StatusUnavailable || report.Code != ReasonConfigurationMissing || report.EvidenceLevel != EvidenceLevel || report.EligibleForPerformanceClaim {
		t.Fatalf("cfg=%#v report=%#v", cfg, report)
	}
}

func TestLoadConfigRejectsNonLoopbackEndpoint(t *testing.T) {
	lookup := func(name string) string {
		return map[string]string{
			"AGENTMESH_BASE_URL":   "http://localhost:8080",
			"AGENTMESH_API_KEY":    "key",
			"AGENTMESH_MODEL":      "model",
			"AGENTMESH_COMMIT_SHA": "e81b658",
		}[name]
	}
	cfg, report := LoadConfig(lookup)
	if cfg != (Config{}) || report.Status != StatusUnavailable || report.Code != ReasonConfigurationInvalid {
		t.Fatalf("cfg=%#v report=%#v", cfg, report)
	}
}
