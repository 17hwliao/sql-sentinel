package mcptool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var testKey = []byte("mcp-test-signing-key-is-not-a-production-credential")

func testToken(t *testing.T, subject, tenant, project, role string, until time.Time) string {
	t.Helper()
	claims := jwt.MapClaims{"iss": "sql-sentinel", "aud": []string{"sql-sentinel"}, "iat": time.Now().Add(-time.Minute).Unix(), "exp": until.Unix(), "jti": "fixture-session-id", "subject": subject, "tenant_id": tenant, "project_id": project, "role": role, "tables": []string{"orders"}}
	raw, e := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(testKey)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}

type taskFixture struct {
	mu                sync.Mutex
	jobs              map[string]map[string]any
	posts, cancels    int
	dropped           bool
	failedAfterCommit bool
	revoked           bool
	started           chan struct{}
	waitCancelled     chan struct{}
}

func (f *taskFixture) serve(w http.ResponseWriter, r *http.Request) {
	claims := jwt.MapClaims{}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	_, e := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return testKey, nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithIssuer("sql-sentinel"), jwt.WithAudience("sql-sentinel"))
	f.mu.Lock()
	revoked := f.revoked
	f.mu.Unlock()
	if e != nil || revoked {
		http.Error(w, `{"code":"unauthorized","private":"do not reflect"}`, 401)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/jobs" && r.Method == "POST" {
		var in map[string]any
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			w.WriteHeader(400)
			return
		}
		id, _ := in["delivery_id"].(string)
		if in["execution_mode"] != "agent.v1" || in["pr_number"] != float64(1) || in["candidate_spec"] != nil {
			w.WriteHeader(400)
			return
		}
		f.mu.Lock()
		old, exists := f.jobs[id]
		if exists && old["sql"] != in["sql"] {
			f.mu.Unlock()
			w.WriteHeader(409)
			return
		}
		if !exists {
			f.posts++
			f.jobs[id] = map[string]any{"job_id": fmt.Sprintf("fixture_job_%d", f.posts), "delivery_id": id, "status": "QUEUED", "sql": in["sql"], "subject": claims["subject"], "tenant": claims["tenant_id"], "project": claims["project_id"]}
		}
		actualJobID := f.jobs[id]["job_id"]
		drop := strings.Contains(in["sql"].(string), "drop_response") && !f.dropped
		commit503 := strings.Contains(in["sql"].(string), "commit_503") && !f.failedAfterCommit
		if commit503 {
			f.failedAfterCommit = true
		}
		if drop {
			f.dropped = true
		}
		f.mu.Unlock()
		if commit503 {
			w.WriteHeader(503)
			w.Write([]byte(`{"code":"store_unavailable","private":"commit succeeded but follow-up read failed"}`))
			return
		}
		if drop {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		if strings.Contains(in["sql"].(string), "wait_response") {
			select {
			case f.started <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			select {
			case f.waitCancelled <- struct{}{}:
			default:
			}
			return
		}
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(map[string]any{"job_id": actualJobID, "delivery_id": id, "duplicate": exists, "status": "QUEUED"})
		return
	}
	byID := strings.HasPrefix(r.URL.Path, "/jobs/by-id/")
	prefix := "/jobs/"
	if byID {
		prefix = "/jobs/by-id/"
	}
	path := strings.TrimPrefix(r.URL.Path, prefix)
	id, suffix, _ := strings.Cut(path, "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	job, ok := f.jobs[id]
	if byID {
		ok = false
		for _, candidate := range f.jobs {
			if candidate["job_id"] == id {
				job = candidate
				ok = true
				break
			}
		}
	}
	if !ok {
		w.WriteHeader(404)
		return
	}
	if claims["tenant_id"] != job["tenant"] || claims["project_id"] != job["project"] || claims["subject"] != job["subject"] {
		w.WriteHeader(403)
		w.Write([]byte(`{"code":"forbidden","private":"raw SQL and secrets must not be reflected"}`))
		return
	}
	if suffix == "cancel" && r.Method == "POST" {
		f.cancels++
		job["status"] = "CANCELLED"
		json.NewEncoder(w).Encode(map[string]any{"status": "CANCELLED", "job_id": id})
		return
	}
	if suffix == "report" {
		w.Header().Set("X-Sentinel-Report-SHA256", strings.Repeat("a", 64))
		json.NewEncoder(w).Encode(map[string]any{"job_id": id, "candidate_spec": nil, "agent": map[string]any{"status": "no_change", "evidence_level": "L1", "agent_trace": []any{}}})
		return
	}
	if r.Method != "GET" {
		w.WriteHeader(405)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"job_id": id, "status": job["status"]})
}

func fixture(t *testing.T) (Config, *taskFixture) {
	t.Helper()
	f := &taskFixture{jobs: map[string]map[string]any{}, started: make(chan struct{}, 1), waitCancelled: make(chan struct{}, 1)}
	httpServer := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	file := filepath.Join(dir, "writer.jwt")
	if e := os.WriteFile(file, []byte(testToken(t, "writer_a", "tenant_a", "project_a", "writer", time.Now().Add(30*time.Minute))), 0600); e != nil {
		t.Fatal(e)
	}
	return Config{APIOrigin: httpServer.URL, TenantID: "tenant_a", ProjectID: "project_a", TokenFile: file, AllowLoopbackHTTP: true}, f
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("SQL_SENTINEL_MCP_TEST_HELPER") != "1" {
		return
	}
	c, err := LoadConfig(os.Getenv("SQL_SENTINEL_MCP_CONFIG_FILE"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "test helper config invalid")
		os.Exit(2)
	}
	s, err := NewServer(c)
	if err != nil {
		fmt.Fprintln(os.Stderr, "test helper credential invalid")
		os.Exit(2)
	}
	if err = s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "test helper stdio closed")
		os.Exit(2)
	}
	os.Exit(0)
}

func connectHelper(t *testing.T, c Config) *mcp.ClientSession {
	t.Helper()
	raw, _ := json.Marshal(c)
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMCPHelperProcess$")
	cmd.Env = append(os.Environ(), "SQL_SENTINEL_MCP_TEST_HELPER=1", "SQL_SENTINEL_MCP_CONFIG_FILE="+file)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "official-sdk-integration", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("official stdio negotiation failed: %v; stderr=%s", err, stderr.String())
	}
	t.Cleanup(func() {
		session.Close()
		if strings.Contains(stderr.String(), "Bearer ") {
			t.Error("private credential leaked to stderr")
		}
	})
	return session
}
func callTool(t *testing.T, s *mcp.ClientSession, tool string, args any) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := s.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Content) != 1 {
		t.Fatalf("expected bounded text result: %+v", got)
	}
	content, ok := got.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatal("non-text response")
	}
	var value map[string]any
	if e := json.Unmarshal([]byte(content.Text), &value); e != nil {
		t.Fatal(e)
	}
	return got, value
}

func TestOfficialSDKStdioSubmitStatusReportCancelAndIdempotency(t *testing.T) {
	c, f := fixture(t)
	session := connectHelper(t, c)
	listed, e := session.ListTools(context.Background(), nil)
	if e != nil || len(listed.Tools) != 4 {
		t.Fatalf("tool discovery: %v %+v", e, listed)
	}
	args := map[string]any{"request_id": "stable-1", "sql": "SELECT id FROM orders WHERE user_id=42"}
	res, value := callTool(t, session, "submit_sql_diagnosis", args)
	if res.IsError {
		t.Fatalf("submit: %+v", value)
	}
	id := value["job_id"].(string)
	if value["delivery_id"] != DeliveryID(c, "stable-1") {
		t.Fatal("submission did not use configured scope")
	}
	res, value = callTool(t, session, "submit_sql_diagnosis", args)
	if res.IsError || value["task"].(map[string]any)["duplicate"] != true {
		t.Fatalf("idempotency: %+v", value)
	}
	res, value = callTool(t, session, "get_diagnosis_status", map[string]any{"job_id": id})
	if res.IsError || value["result"].(map[string]any)["status"] != "QUEUED" {
		t.Fatalf("status: %+v", value)
	}
	res, value = callTool(t, session, "get_diagnosis_report", map[string]any{"job_id": id})
	if res.IsError || value["report_sha256"] != strings.Repeat("a", 64) || value["result"].(map[string]any)["candidate_spec"] != nil {
		t.Fatalf("no-candidate report: %+v", value)
	}
	res, value = callTool(t, session, "cancel_diagnosis", map[string]any{"job_id": id})
	if res.IsError || value["result"].(map[string]any)["status"] != "CANCELLED" {
		t.Fatalf("cancel: %+v", value)
	}
	res, value = callTool(t, session, "submit_sql_diagnosis", map[string]any{"request_id": "stable-1", "sql": "SELECT status FROM orders"})
	if !res.IsError || value["code"] != "task_conflict" {
		t.Fatalf("changed input silently reused delivery: %+v", value)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 || f.cancels != 1 {
		t.Fatalf("durable mutations duplicated: posts=%d cancels=%d", f.posts, f.cancels)
	}
}

func TestOfficialSDKUnknownSubmissionCanBeQueriedAndRetriedWithoutNewJob(t *testing.T) {
	c, f := fixture(t)
	session := connectHelper(t, c)
	args := map[string]any{"request_id": "unknown-1", "sql": "SELECT id FROM orders /* drop_response */"}
	res, value := callTool(t, session, "submit_sql_diagnosis", args)
	if !res.IsError || value["outcome_unknown"] != true {
		t.Fatalf("unknown result hidden: %+v", value)
	}
	id := value["delivery_id"].(string)
	res, value = callTool(t, session, "get_diagnosis_status", map[string]any{"delivery_id": id})
	if res.IsError {
		t.Fatalf("recovery query failed: %+v", value)
	}
	res, value = callTool(t, session, "submit_sql_diagnosis", args)
	if res.IsError || value["task"].(map[string]any)["duplicate"] != true {
		t.Fatalf("recovery retry: %+v", value)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 {
		t.Fatalf("response loss created %d jobs", f.posts)
	}
}

func TestOfficialSDKCommittedSubmissionWith503KeepsUnknownResultAndStableRecovery(t *testing.T) {
	c, f := fixture(t)
	session := connectHelper(t, c)
	args := map[string]any{"request_id": "commit-503", "sql": "SELECT id FROM orders /* commit_503 */"}
	res, value := callTool(t, session, "submit_sql_diagnosis", args)
	if !res.IsError || value["code"] != "task_service_unavailable" || value["outcome_unknown"] != true || strings.Contains(scenarioText(value), "follow-up read") {
		t.Fatalf("committed 503 was treated as definite rejection or leaked private error: %+v", value)
	}
	id := value["delivery_id"].(string)
	res, value = callTool(t, session, "get_diagnosis_status", map[string]any{"delivery_id": id})
	if res.IsError || value["result"].(map[string]any)["status"] != "QUEUED" {
		t.Fatalf("503 recovery could not find durable submission: %+v", value)
	}
	res, value = callTool(t, session, "submit_sql_diagnosis", args)
	if res.IsError || value["task"].(map[string]any)["duplicate"] != true {
		t.Fatalf("stable 503 retry did not recover job: %+v", value)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 {
		t.Fatalf("503 created duplicate jobs: %d", f.posts)
	}
}

func TestOfficialSDKScopedCredentialReloadAndParameterBoundary(t *testing.T) {
	c, f := fixture(t)
	session := connectHelper(t, c)
	_, value := callTool(t, session, "submit_sql_diagnosis", map[string]any{"request_id": "owner-1", "sql": "SELECT id FROM orders"})
	id := value["job_id"].(string)
	if e := os.WriteFile(c.TokenFile, []byte(testToken(t, "writer_b", "tenant_a", "project_a", "writer", time.Now().Add(30*time.Minute))), 0600); e != nil {
		t.Fatal(e)
	}
	res, value := callTool(t, session, "get_diagnosis_report", map[string]any{"job_id": id})
	if !res.IsError || value["code"] != "forbidden" || strings.Contains(scenarioText(value), "raw SQL") {
		t.Fatalf("ownership/error privacy: %+v", value)
	}
	if e := os.WriteFile(c.TokenFile, []byte(testToken(t, "writer_a", "tenant_a", "project_a", "writer", time.Now().Add(-time.Minute))), 0600); e != nil {
		t.Fatal(e)
	}
	res, value = callTool(t, session, "get_diagnosis_status", map[string]any{"job_id": id})
	if !res.IsError || value["code"] != "credential_expired_or_revoked" {
		t.Fatalf("expiry recovery: %+v", value)
	}
	if e := os.WriteFile(c.TokenFile, []byte(testToken(t, "writer_a", "tenant_b", "project_a", "writer", time.Now().Add(30*time.Minute))), 0600); e != nil {
		t.Fatal(e)
	}
	res, value = callTool(t, session, "get_diagnosis_status", map[string]any{"job_id": id})
	if !res.IsError || value["code"] != "credential_expired_or_revoked" {
		t.Fatalf("credential expanded configured scope: %+v", value)
	}
	if e := os.WriteFile(c.TokenFile, []byte(testToken(t, "writer_a", "tenant_a", "project_a", "writer", time.Now().Add(30*time.Minute))), 0600); e != nil {
		t.Fatal(e)
	}
	res, value = callTool(t, session, "get_diagnosis_status", map[string]any{"job_id": id})
	if res.IsError {
		t.Fatalf("renewed private credential was not read: %+v", value)
	}
	f.mu.Lock()
	f.revoked = true
	f.mu.Unlock()
	res, value = callTool(t, session, "get_diagnosis_status", map[string]any{"job_id": id})
	if !res.IsError || value["code"] != "credential_expired_or_revoked" || strings.Contains(scenarioText(value), "private") {
		t.Fatalf("API-side revocation/error privacy: %+v", value)
	}
	f.mu.Lock()
	f.revoked = false
	f.mu.Unlock()
	for _, args := range []map[string]any{{"job_id": "http://other.invalid/jobs/x"}, {"job_id": id, "project_id": "project_b"}, {"job_id": id, "api_origin": "http://other.invalid"}} {
		res, value = callTool(t, session, "get_diagnosis_status", args)
		if !res.IsError || value["code"] != "invalid_arguments" {
			t.Fatalf("argument widened target/scope: %+v", value)
		}
	}
}
func scenarioText(v any) string { raw, _ := json.Marshal(v); return string(raw) }

func TestOfficialSDKJobAndDeliveryNamespacesCannotCancelEachOther(t *testing.T) {
	c, f := fixture(t)
	f.mu.Lock()
	f.jobs["shared_reference"] = map[string]any{"job_id": "first_job", "delivery_id": "shared_reference", "status": "QUEUED", "subject": "writer_a", "tenant": "tenant_a", "project": "project_a"}
	f.jobs["another_delivery"] = map[string]any{"job_id": "shared_reference", "delivery_id": "another_delivery", "status": "QUEUED", "subject": "writer_a", "tenant": "tenant_a", "project": "project_a"}
	f.mu.Unlock()
	session := connectHelper(t, c)
	res, value := callTool(t, session, "cancel_diagnosis", map[string]any{"job_id": "shared_reference"})
	if res.IsError {
		t.Fatalf("explicit job cancellation failed: %+v", value)
	}
	res, value = callTool(t, session, "get_diagnosis_status", map[string]any{"delivery_id": "shared_reference"})
	if res.IsError || value["result"].(map[string]any)["status"] != "QUEUED" {
		t.Fatalf("job_id cancelled same-named delivery_id: %+v", value)
	}
	res, value = callTool(t, session, "get_diagnosis_status", map[string]any{"job_id": "shared_reference"})
	if res.IsError || value["result"].(map[string]any)["status"] != "CANCELLED" {
		t.Fatalf("job namespace selected wrong task: %+v", value)
	}
	res, value = callTool(t, session, "cancel_diagnosis", map[string]any{"job_id": "shared_reference", "delivery_id": "shared_reference"})
	if !res.IsError || value["code"] != "invalid_arguments" {
		t.Fatalf("ambiguous identifier accepted: %+v", value)
	}
}

func TestOfficialSDKCancellingToolWaitDoesNotCancelDurableJob(t *testing.T) {
	c, f := fixture(t)
	session := connectHelper(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "submit_sql_diagnosis", Arguments: map[string]any{"request_id": "cancel-wait", "sql": "SELECT id FROM orders /* wait_response */"}})
		done <- err
	}()
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("submit did not reach task service")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled tool wait did not return")
	}
	select {
	case <-f.waitCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("MCP cancellation did not cancel the HTTP wait")
	}
	id := DeliveryID(c, "cancel-wait")
	res, value := callTool(t, session, "get_diagnosis_status", map[string]any{"delivery_id": id})
	if res.IsError || value["result"].(map[string]any)["status"] != "QUEUED" {
		t.Fatalf("tool cancellation cancelled durable work: %+v", value)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancels != 0 || f.posts != 1 {
		t.Fatalf("implicit cancellation: posts=%d cancels=%d", f.posts, f.cancels)
	}
}

func TestConfigurationRejectsArbitraryHTTPAndAdministrativeCredentials(t *testing.T) {
	c, _ := fixture(t)
	for _, origin := range []string{"http://public.example.test", "https://api.example.test/path", "https://user:secret@api.example.test", "https://api.example.test?token=bad"} {
		bad := c
		bad.APIOrigin = origin
		if _, err := NewServer(bad); err == nil {
			t.Errorf("unsafe origin accepted: %s", origin)
		}
	}
	for _, token := range []string{"bootstrap-do-not-store", testToken(t, "operator", "tenant_a", "project_a", "admin", time.Now().Add(30*time.Minute))} {
		if err := os.WriteFile(c.TokenFile, []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewServer(c); err == nil {
			t.Fatal("administrator/bootstrap accepted")
		}
	}
}

func TestRedirectCannotSendCredentialToAnotherOrigin(t *testing.T) {
	c, _ := fixture(t)
	received := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received++; w.Write([]byte(`{}`)) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	c.APIOrigin = redirect.URL
	session := connectHelper(t, c)
	res, value := callTool(t, session, "get_diagnosis_status", map[string]any{"job_id": "valid-job"})
	if !res.IsError || value["code"] != "task_service_rejected" || received != 0 {
		t.Fatalf("redirect followed: received=%d result=%+v", received, value)
	}
}
