package mcptool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The production Caddy template strips /sentinel before forwarding. The MCP
// client must address that mount, rather than call the gateway's root /jobs.
func TestPublicDeploymentPrefix(t *testing.T) {
	original, f := fixture(t)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sentinel/connections" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"allowed_tables":["orders"]}`))
			return
		}
		mux := http.NewServeMux()
		mux.Handle("/sentinel/", http.StripPrefix("/sentinel", http.HandlerFunc(f.serve)))
		mux.ServeHTTP(w, r)
	}))
	defer proxy.Close()
	raw, _ := json.Marshal(original)
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	doc["api_origin"] = proxy.URL
	doc["api_base_path"] = "/sentinel"
	raw, _ = json.Marshal(doc)
	file := filepath.Join(t.TempDir(), "mcp.json")
	if os.WriteFile(file, raw, 0600) != nil {
		t.Fatal("private fixture config write failed")
	}
	cfg, err := LoadConfig(file)
	if err != nil {
		t.Fatal("public deployment's fixed Sentinel mount rejected")
	}
	if err = Preflight(context.Background(), cfg); err != nil {
		t.Fatal("preflight did not use Sentinel mount")
	}
	session := connectHelper(t, cfg)
	out, v := callTool(t, session, "submit_sql_diagnosis", map[string]any{"request_id": "prefix_case", "sql": "SELECT id FROM orders"})
	if out.IsError {
		t.Fatal("mounted submit failed")
	}
	id, _ := v["job_id"].(string)
	if out, _ = callTool(t, session, "get_diagnosis_status", map[string]any{"job_id": id}); out.IsError {
		t.Fatal("mounted status failed")
	}
	out, v = callTool(t, session, "get_diagnosis_report", map[string]any{"job_id": id})
	if out.IsError || v["full_report_path"] != "/sentinel/jobs/by-id/"+id+"/report" {
		t.Fatal("mounted report path lost deployment prefix")
	}
	if out, _ = callTool(t, session, "cancel_diagnosis", map[string]any{"job_id": id}); out.IsError {
		t.Fatal("mounted cancel failed")
	}
}

func TestDeploymentPrefixCannotOverrideDestination(t *testing.T) {
	c, _ := fixture(t)
	for _, path := range []string{"/", "//evil.test", "/sentinel/../auth", "/sentinel?scope=other", "/sentinel/", "https://evil.test"} {
		c.APIBasePath = path
		if validateConfig(c) == nil {
			t.Fatalf("unsafe base path accepted: %s", path)
		}
	}
	c.APIBasePath = ""
	if validateConfig(c) != nil {
		t.Fatal("legacy origin-only profile rejected")
	}
}
