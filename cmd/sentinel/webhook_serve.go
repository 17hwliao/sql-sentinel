package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"sqlsentinel/internal/candidate"
	"sqlsentinel/internal/pipeline"
	"sqlsentinel/internal/snapshot"
	"sqlsentinel/internal/webhook"
)

func runWebhookServe(args []string) error {
	fs := flag.NewFlagSet("webhook-serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "loopback address for the local webhook service")
	candidatePath := fs.String("candidate", "", "local CandidateSpec JSON file")
	outputRoot := fs.String("out-dir", "", "directory for local delivery comments and artifacts")
	maxConcurrent := fs.Int("max-concurrent", 1, "maximum concurrent shadow validations (1-4)")
	chunk := fs.Int("chunk", snapshot.DefaultChunk, "snapshot chunk size")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateWebhookListen(*listen); err != nil {
		return err
	}
	if *candidatePath == "" || *outputRoot == "" {
		return errors.New("--candidate and --out-dir are required")
	}
	if *maxConcurrent < 1 || *maxConcurrent > 4 {
		return errors.New("--max-concurrent must be between 1 and 4")
	}
	secret := os.Getenv("SQL_SENTINEL_WEBHOOK_SECRET")
	if secret == "" {
		return errors.New("SQL_SENTINEL_WEBHOOK_SECRET is required")
	}
	candidateRaw, err := os.ReadFile(*candidatePath)
	if err != nil {
		return fmt.Errorf("read CandidateSpec: %w", err)
	}
	spec, err := candidate.DecodeStrict(bytes.NewReader(candidateRaw))
	if err != nil {
		return err
	}
	if err := candidate.Validate(spec); err != nil {
		return err
	}
	server, err := webhook.New(webhook.Config{
		Secret: []byte(secret), OutputRoot: *outputRoot, MaxConcurrent: *maxConcurrent,
		RunPipeline: func(ctx context.Context, sql []byte, artifacts string) (pipeline.Report, error) {
			return pipeline.Run(ctx, pipeline.Input{
				SQL: sql, CandidateSpec: candidateRaw, OutputDir: artifacts,
				RunExplain: pipelineExplainRunner(*chunk),
			})
		},
	})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
	}
	fmt.Printf("webhook service listening on http://%s%s (local-only, max_concurrent=%d)\n", *listen, webhook.Path, *maxConcurrent)
	err = httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func validateWebhookListen(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" || port == "" {
		return errors.New("--listen must be a 127.0.0.1 host:port address")
	}
	return nil
}
