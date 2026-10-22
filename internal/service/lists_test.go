package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"net/http/httptest"
	"os"
	"sqlsentinel/internal/identity"
	workflow "sqlsentinel/internal/kafka"
	"sqlsentinel/internal/rag"
	"strings"
	"testing"
	"time"
)

func TestRealScopedConsoleLists(t *testing.T) {
	dsn := os.Getenv("SQL_SENTINEL_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("explicit isolated MySQL required")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store := &workflow.MySQLStore{DB: db}
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	tenant := "console_" + suffix
	auth, err := identity.New(db, []byte(strings.Repeat("k", 32)), strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := auth.Issue(ctx, identity.Principal{Subject: "reader", Tenant: tenant, Project: "a", Role: "reader", Tables: []string{"orders"}})
	if err != nil {
		t.Fatal(err)
	}
	admin, _, err := auth.Issue(ctx, identity.Principal{Subject: "admin", Tenant: tenant, Project: "a", Role: "admin", Tables: []string{"orders"}})
	if err != nil {
		t.Fatal(err)
	}
	embed, _ := rag.NewFakeEmbedding(64)
	knowledge, _ := rag.NewService(embed, &rag.MySQLVectorStore{DB: db, Dim: 64}, rag.ChunkConfig{})
	for i, project := range []string{"a", "b", "a"} {
		query := "SELECT id FROM orders"
		if i == 2 {
			query = "SELECT id FROM secrets"
		}
		_, err = store.Receive(ctx, workflow.ReceiveInput{DeliveryID: "console_" + suffix + fmt.Sprint(i), PRNumber: 1, SQL: []byte(query), CandidateSpec: []byte("{}"), Options: workflow.JobOptions{TenantID: tenant, ProjectID: project, Actor: "writer"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, project := range []string{"a", "b"} {
		_, _, err = knowledge.Ingest(ctx, rag.IngestRequest{DocumentID: "doc_" + project, Scope: rag.Scope{TenantID: tenant, ProjectID: project}, SourceURI: "console:test", Version: "v2", ContentType: "text/plain", Content: "controlled backup evidence for scoped project"})
		if err != nil {
			t.Fatal(err)
		}
	}
	handler := (&Server{Store: store, Auth: auth, RAG: knowledge}).Handler()
	request := func(path, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	for _, path := range []string{"/jobs", "/rag/documents"} {
		w := request(path, token)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var page struct {
			Data []map[string]any `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Data) != 1 {
			t.Fatalf("scope/table leak: %s", w.Body.String())
		}
		if strings.Contains(w.Body.String(), "sql_text") {
			t.Fatal("list exposes SQL")
		}
	}
	for _, path := range []string{"/jobs?limit=0", "/jobs?status=UNKNOWN", "/rag/documents?after=bad%2Fcursor"} {
		if w := request(path, token); w.Code != 400 {
			t.Fatalf("invalid query accepted: %s %d", path, w.Code)
		}
	}
	if w := request("/auth/sessions", token); w.Code != 403 {
		t.Fatal("reader can inspect sessions")
	}
	if w := request("/auth/sessions", admin); w.Code != 200 || strings.Contains(w.Body.String(), `"token":`) {
		t.Fatalf("session metadata: %d %s", w.Code, w.Body.String())
	}
	if w := request("/connections", token); w.Code != 200 || !strings.Contains(w.Body.String(), `"github_configured":false`) {
		t.Fatal("connection status unavailable")
	}
}
