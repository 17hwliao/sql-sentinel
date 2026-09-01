package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"sqlsentinel/internal/agentmeshbridge"
)

func runAgentMeshBridgeTest(args []string) error {
	fs := flag.NewFlagSet("agentmesh-bridge-test", flag.ContinueOnError)
	out := fs.String("out", "", "new bridge report JSON path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("--out is required")
	}

	cfg, report := agentmeshbridge.LoadConfig(os.Getenv)
	if report.Status == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 95*time.Second)
		defer cancel()
		report = agentmeshbridge.Run(ctx, &http.Client{}, cfg)
	}
	if err := writeNewBridgeReport(*out, report); err != nil {
		return err
	}
	fmt.Printf("AgentMesh bridge report: %s (status=%s, code=%s, L1, performance claim eligible=false)\n", *out, report.Status, report.Code)
	if report.Status != agentmeshbridge.StatusCompleted {
		return fmt.Errorf("AgentMesh bridge rejected: %s", report.Code)
	}
	return nil
}

func writeNewBridgeReport(path string, report agentmeshbridge.Report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create new bridge report: %w", err)
	}
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		_ = f.Close()
		return fmt.Errorf("write bridge report: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close bridge report: %w", err)
	}
	return nil
}
