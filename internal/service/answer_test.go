package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sqlsentinel/internal/rag"
	"strings"
	"testing"
)

type answerStub string

func (s answerStub) Draft(context.Context, string) (string, error) { return string(s), nil }

func TestAnswerRejectsInventedCitationsAndRefusesUnrelatedEvidence(t *testing.T) {
	embed, _ := rag.NewFakeEmbedding(64)
	store, _ := rag.NewMemoryVectorStore(64)
	knowledge, _ := rag.NewService(embed, store, rag.ChunkConfig{})
	_, chunks, err := knowledge.Ingest(context.Background(), rag.IngestRequest{DocumentID: "doc", Scope: rag.Scope{TenantID: "operator", ProjectID: "default"}, SourceURI: "urn:runbook", Version: "v1", ContentType: "text/plain", Content: "verified backup restores persistent storage"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, text string
		status     int
	}{
		{"forged", `{"answer":"claim","citation_chunk_ids":["invented"]}`, 502},
		{"no_citation", `{"answer":"claim","citation_chunk_ids":[]}`, 200},
		{"cited", `{"answer":"backup restores storage","citation_chunk_ids":["` + chunks[0].ID + `"]}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := (&Server{Token: "private-test", RAG: knowledge, AnswerModel: answerStub(tc.text)}).Handler()
			req := httptest.NewRequest("POST", "/rag/answer", strings.NewReader(`{"question":"verified backup restores persistent storage"}`))
			req.Header.Set("Authorization", "Bearer private-test")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status %d body %s", w.Code, w.Body.String())
			}
			if tc.name == "no_citation" {
				var body map[string]any
				_ = json.Unmarshal(w.Body.Bytes(), &body)
				if body["has_evidence"] != false {
					t.Fatal("unsupported claim accepted")
				}
			}
		})
	}
	h := (&Server{Token: "private-test", RAG: knowledge, AnswerModel: answerStub(`{"answer":"unsupported","citation_chunk_ids":[]}`)}).Handler()
	req := httptest.NewRequest("POST", "/rag/answer", strings.NewReader(`{"question":"photosynthesis chlorophyll"}`))
	req.Header.Set("Authorization", "Bearer private-test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != 200 || body["has_evidence"] != false {
		t.Fatalf("unrelated evidence accepted: %d %s", w.Code, w.Body.String())
	}
}
