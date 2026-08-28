package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sqlsentinel/internal/pipeline"
)

const testSecret = "test-webhook-secret"

func TestMissingSignatureDoesNotReadOrProcessBody(t *testing.T) {
	root := t.TempDir()
	calls := 0
	server := newTestServer(t, root, func(context.Context, []byte, string) (pipeline.Report, error) {
		calls++
		return pipeline.Report{}, nil
	})
	body := &readTracker{Reader: strings.NewReader(`{"not":"an event"}`)}
	req := httptest.NewRequest(http.MethodPost, Path, body)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized || rejectionCode(t, rec) != ReasonSignatureMissing {
		t.Fatalf("unexpected rejection: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if body.read {
		t.Fatal("missing signature read a body that must not be semantically processed")
	}
	if calls != 0 || deliveryEntries(t, root) != 0 {
		t.Fatalf("missing signature processed delivery: calls=%d entries=%d", calls, deliveryEntries(t, root))
	}
}

func TestInvalidSignatureNeverDecodesOrProcessesDelivery(t *testing.T) {
	root := t.TempDir()
	calls := 0
	server := newTestServer(t, root, func(context.Context, []byte, string) (pipeline.Report, error) {
		calls++
		return pipeline.Report{}, nil
	})
	req := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(`{"not":"an event"}`))
	req.Header.Set(SignatureHeader, "sha256="+strings.Repeat("0", 64))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized || rejectionCode(t, rec) != ReasonInvalidSignature {
		t.Fatalf("unexpected rejection: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 0 || deliveryEntries(t, root) != 0 {
		t.Fatalf("invalid signature processed delivery: calls=%d entries=%d", calls, deliveryEntries(t, root))
	}
}

func TestBodyLimitRejectsBeforeEventProcessing(t *testing.T) {
	root := t.TempDir()
	calls := 0
	server := newTestServer(t, root, func(context.Context, []byte, string) (pipeline.Report, error) {
		calls++
		return pipeline.Report{}, nil
	})
	body := []byte(strings.Repeat("x", int(MaxBodyBytes)+1))
	rec := perform(server, body)
	if rec.Code != http.StatusRequestEntityTooLarge || rejectionCode(t, rec) != ReasonBodyTooLarge {
		t.Fatalf("unexpected rejection: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 0 || deliveryEntries(t, root) != 0 {
		t.Fatal("oversized body reached delivery processing")
	}
}

func TestRejectedAndMultipleSQLNeverReachPipeline(t *testing.T) {
	cases := map[string]struct {
		diff     string
		wantCode string
	}{
		"locking":  {"diff --git a/q.sql b/q.sql\n+SELECT * FROM orders FOR UPDATE\n", "locking_read"},
		"multiple": {"diff --git a/a.sql b/a.sql\n+SELECT 1\ndiff --git a/b.sql b/b.sql\n+SELECT 2\n", ReasonMultipleSQL},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			calls := 0
			server := newTestServer(t, root, func(context.Context, []byte, string) (pipeline.Report, error) {
				calls++
				return pipeline.Report{}, nil
			})
			rec := perform(server, eventJSON(t, "delivery-1", tc.diff))
			if rec.Code != http.StatusAccepted || calls != 0 {
				t.Fatalf("unexpected pipeline call/status: calls=%d status=%d", calls, rec.Code)
			}
			comment := readComment(t, root, "delivery-1")
			if !strings.Contains(comment, "`verification_unavailable`") || !strings.Contains(comment, "`"+tc.wantCode+"`") {
				t.Fatalf("comment does not record unavailable validation %q: %s", tc.wantCode, comment)
			}
			if _, err := os.Stat(filepath.Join(root, "delivery-1", "artifacts")); !os.IsNotExist(err) {
				t.Fatal("SQL rejection created pipeline artifacts")
			}
		})
	}
}

func TestAcceptedSQLRunsOnceAndRendersL1ArtifactHashes(t *testing.T) {
	root := t.TempDir()
	calls := 0
	server := newTestServer(t, root, func(_ context.Context, sql []byte, output string) (pipeline.Report, error) {
		calls++
		if got := string(sql); got != "SELECT * FROM orders" {
			t.Fatalf("runner SQL = %q", got)
		}
		if filepath.Base(output) != "artifacts" {
			t.Fatalf("runner output = %q", output)
		}
		return pipeline.Report{
			EvidenceLevel: "L1", EligibleForPerformanceClaim: false,
			CompletedSteps:   []string{"sql_admit", "candidate_explain"},
			RejectionReasons: []pipeline.Reason{},
			Artifacts:        []pipeline.Artifact{{Step: "sql_admit", File: "sql_admission.json", SHA256: "abc123"}},
		}, nil
	})
	rec := perform(server, eventJSON(t, "delivery-ok", "diff --git a/q.sql b/q.sql\n+SELECT * FROM orders\n"))
	if rec.Code != http.StatusAccepted || calls != 1 {
		t.Fatalf("unexpected pipeline call/status: calls=%d status=%d", calls, rec.Code)
	}
	comment := readComment(t, root, "delivery-ok")
	for _, wanted := range []string{"`verification_completed`", "`L1`", "`false`", "`sql_admit`", "`candidate_explain`", "`abc123`"} {
		if !strings.Contains(comment, wanted) {
			t.Fatalf("comment missing %q: %s", wanted, comment)
		}
	}
}

func TestPipelineFailureIsExplicitlyUnavailable(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t, root, func(context.Context, []byte, string) (pipeline.Report, error) {
		return pipeline.Report{}, io.ErrUnexpectedEOF
	})
	rec := perform(server, eventJSON(t, "delivery-failed", "diff --git a/q.sql b/q.sql\n+SELECT 1\n"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	comment := readComment(t, root, "delivery-failed")
	for _, wanted := range []string{"`verification_unavailable`", "`" + ReasonPipelineFailed + "`"} {
		if !strings.Contains(comment, wanted) {
			t.Fatalf("comment missing %q: %s", wanted, comment)
		}
	}
	if strings.Contains(comment, "evidence_level: `L1`") || !strings.Contains(comment, "evidence_level: `none`") {
		t.Fatalf("failed pipeline must not claim an evidence level: %s", comment)
	}
	var report DeliveryReport
	data, err := os.ReadFile(filepath.Join(root, "delivery-failed", "webhook_report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.EvidenceLevel != "" {
		t.Fatalf("failed pipeline evidence level = %q, want empty", report.EvidenceLevel)
	}
}

func TestExistingDeliveryReturnsStableCodeOnly(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t, root, func(context.Context, []byte, string) (pipeline.Report, error) {
		t.Fatal("existing delivery must not reach pipeline")
		return pipeline.Report{}, nil
	})
	if err := os.Mkdir(filepath.Join(root, "delivery-existing"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := perform(server, eventJSON(t, "delivery-existing", "diff --git a/q.sql b/q.sql\n+SELECT 1\n"))
	if rec.Code != http.StatusConflict || rejectionCode(t, rec) != ReasonDeliveryExists {
		t.Fatalf("unexpected existing-delivery response: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestQueueFullReturns429WithoutSecondPipelineOrOutput(t *testing.T) {
	root := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	server := newTestServer(t, root, func(context.Context, []byte, string) (pipeline.Report, error) {
		calls++
		close(started)
		<-release
		return pipeline.Report{EvidenceLevel: "L1", RejectionReasons: []pipeline.Reason{}, Artifacts: []pipeline.Artifact{}}, nil
	})
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- perform(server, eventJSON(t, "delivery-first", "diff --git a/q.sql b/q.sql\n+SELECT 1\n"))
	}()
	<-started

	second := perform(server, eventJSON(t, "delivery-second", "diff --git a/q.sql b/q.sql\n+SELECT 2\n"))
	if second.Code != http.StatusTooManyRequests || rejectionCode(t, second) != ReasonQueueFull {
		t.Fatalf("unexpected queue result: status=%d body=%s", second.Code, second.Body.String())
	}
	if calls != 1 {
		t.Fatalf("pipeline calls = %d, want 1", calls)
	}
	if _, err := os.Stat(filepath.Join(root, "delivery-second")); !os.IsNotExist(err) {
		t.Fatal("queue-full delivery created an output directory")
	}
	close(release)
	if first := <-firstDone; first.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
}

func newTestServer(t *testing.T, root string, runner PipelineRunner) *Server {
	t.Helper()
	server, err := New(Config{Secret: []byte(testSecret), OutputRoot: root, MaxConcurrent: 1, RunPipeline: runner})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func eventJSON(t *testing.T, id, diff string) []byte {
	t.Helper()
	body, err := json.Marshal(Event{DeliveryID: id, PRNumber: 7, Diff: diff})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func perform(server *Server, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, Path, bytes.NewReader(body))
	mac := hmac.New(sha256.New, []byte(testSecret))
	_, _ = mac.Write(body)
	req.Header.Set(SignatureHeader, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func rejectionCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out["rejection_code"]
}

func deliveryEntries(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func readComment(t *testing.T, root, id string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, id, "comment.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type readTracker struct {
	io.Reader
	read bool
}

func (r *readTracker) Read(p []byte) (int, error) {
	r.read = true
	return r.Reader.Read(p)
}

func (r *readTracker) Close() error { return nil }
