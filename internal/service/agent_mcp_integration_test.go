package service

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sqlsentinel/internal/mcptool"
)

func TestMCPMySQLStdioHelper(t *testing.T) {
	if os.Getenv("SQL_SENTINEL_MCP_MYSQL_HELPER") != "1" {
		return
	}
	c, e := mcptool.LoadConfig(os.Getenv("SQL_SENTINEL_MCP_CONFIG_FILE"))
	if e != nil {
		os.Exit(2)
	}
	server, e := mcptool.NewServer(c)
	if e != nil {
		os.Exit(2)
	}
	if server.Run(context.Background(), &mcp.StdioTransport{}) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestMCPRealMySQLIntegration(t *testing.T) {
	f := openAgentIntegration(t)
	f.resetOrders()
	private := t.TempDir()
	tokenFile := filepath.Join(private, "writer.jwt")
	if os.WriteFile(tokenFile, []byte(f.writer), 0600) != nil {
		t.Fatal("private test credential file failed")
	}
	cfg := mcptool.Config{APIOrigin: f.server.URL, TenantID: f.tenant, ProjectID: f.project, TokenFile: tokenFile, AllowLoopbackHTTP: true}
	raw, _ := json.Marshal(cfg)
	configFile := filepath.Join(private, "mcp.json")
	if os.WriteFile(configFile, raw, 0600) != nil {
		t.Fatal("private config write failed")
	}
	command := exec.Command(os.Args[0], "-test.run=^TestMCPMySQLStdioHelper$")
	command.Env = append(os.Environ(), "SQL_SENTINEL_MCP_MYSQL_HELPER=1", "SQL_SENTINEL_MCP_CONFIG_FILE="+configFile)
	client := mcp.NewClient(&mcp.Implementation{Name: "actual-mysql-integration", Version: "1"}, nil)
	ctx, stop := context.WithTimeout(context.Background(), 45*time.Second)
	defer stop()
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal("official MCP stdio negotiation failed")
	}
	defer session.Close()
	call := func(name string, args any) map[string]any {
		t.Helper()
		out, e := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if e != nil || out.IsError || len(out.Content) != 1 {
			t.Fatalf("MCP actual service call failed: %s", name)
		}
		text, ok := out.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatal("invalid protocol content")
		}
		var body map[string]any
		if json.Unmarshal([]byte(text.Text), &body) != nil {
			t.Fatal("invalid protocol JSON")
		}
		return body
	}
	input := map[string]any{"request_id": "actual_mysql", "sql": "SELECT id FROM orders WHERE user_id=42 AND payload='needle'"}
	submitted := call("submit_sql_diagnosis", input)
	id, ok := submitted["job_id"].(string)
	if !ok {
		t.Fatal("missing actual durable job ID")
	}
	if again := call("submit_sql_diagnosis", input); again["job_id"] != id {
		t.Fatal("stable MCP submit duplicated task")
	}
	call("get_diagnosis_status", map[string]any{"job_id": id})
	job, e := f.store.GetByID(ctx, id)
	if e != nil {
		t.Fatal("MCP did not reach real task store")
	}
	inner, calls := compatibleFixtureNode(t, true)
	f.run(job, inner)
	f.assertClean()
	report := call("get_diagnosis_report", map[string]any{"job_id": id})
	projected, ok := report["result"].(map[string]any)
	if !ok || report["report_projection"] != true || projected["outcome"] != "recommended" || calls.Load() != 3 {
		t.Fatal("MCP did not return actual inner verification evidence")
	}
	input["request_id"] = "actual_cancel"
	cancelled := call("submit_sql_diagnosis", input)
	cancelID := cancelled["job_id"].(string)
	call("cancel_diagnosis", map[string]any{"job_id": cancelID})
	actual, e := f.store.GetByID(ctx, cancelID)
	if e != nil || string(actual.Status) != "CANCELLED" {
		t.Fatal("MCP explicit cancel did not persist")
	}
}
