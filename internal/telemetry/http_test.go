package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamingAndFiniteLabels(t *testing.T) {
	r, e := New(context.Background(), "test", "")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close(context.Background())
	h := r.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, e := w.Write([]byte("data: x\n\n")); e != nil {
			t.Fatal(e)
		}
		w.(http.Flusher).Flush()
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/jobs/private-secret-id", nil))
	if w.Body.String() != "data: x\n\n" || !w.Flushed {
		t.Fatal("SSE wrapper broke flushing")
	}
	metrics := httptest.NewRecorder()
	r.Metrics().ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
	if strings.Contains(metrics.Body.String(), "private-secret-id") || !strings.Contains(metrics.Body.String(), "backend_http_requests_total") {
		t.Fatal(metrics.Body.String())
	}
}
