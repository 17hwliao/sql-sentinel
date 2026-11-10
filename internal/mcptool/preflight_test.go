package mcptool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestReadOnlyPreflight(t *testing.T) {
	c, _ := fixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/connections" || r.Method != "GET" || r.Header.Get("Authorization") == "" {
			t.Error("preflight mutated task service or omitted credential")
			w.WriteHeader(400)
			return
		}
		w.Write([]byte(`{"allowed_tables":["orders"]}`))
	}))
	defer server.Close()
	c.APIOrigin = server.URL
	if err := Preflight(context.Background(), c); err != nil || calls.Load() != 1 {
		t.Fatalf("read-only check err=%v calls=%d", err, calls.Load())
	}
}

func TestReadOnlyPreflightRevokedCredential(t *testing.T) {
	c, f := fixture(t)
	f.mu.Lock()
	f.revoked = true
	f.mu.Unlock()
	if err := Preflight(context.Background(), c); err == nil || err.Error() != "credential_expired_or_revoked" {
		t.Fatalf("revocation hidden: %v", err)
	}
}
