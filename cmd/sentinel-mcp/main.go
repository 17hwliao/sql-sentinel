// sentinel-mcp is a local stdio adapter. Standard output is exclusively MCP.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sqlsentinel/internal/mcptool"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	c, err := mcptool.LoadConfig(os.Getenv("SQL_SENTINEL_MCP_CONFIG_FILE"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "sentinel-mcp: configuration invalid; see docs/mcp.md")
		os.Exit(2)
	}
	if len(os.Args) == 2 && os.Args[1] == "--check" {
		if mcptool.Preflight(context.Background(), c) != nil {
			fmt.Fprintln(os.Stderr, "sentinel-mcp: read-only credential/task-service preflight failed")
			os.Exit(2)
		}
		fmt.Println(`{"status":"ready","paid_model_calls":0,"job_submissions":0,"check":"local credential shape and authenticated GET /connections; model and cumulative budget not checked"}`)
		return
	}
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "sentinel-mcp: only --check or stdio mode is supported")
		os.Exit(2)
	}
	server, err := mcptool.NewServer(c)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sentinel-mcp: scoped writer credential invalid or expired; replace the private token file and restart")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err = server.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "sentinel-mcp: stdio session ended")
		os.Exit(1)
	}
}
