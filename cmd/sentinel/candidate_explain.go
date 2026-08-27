package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"sqlsentinel/internal/candidate"
	"sqlsentinel/internal/shadowgate"
	"sqlsentinel/internal/snapshot"
)

func runCandidateExplain(args []string) error {
	fs := flag.NewFlagSet("candidate-explain", flag.ExitOnError)
	sqlPath := fs.String("sql", "", "admitted single read-only SQL file")
	candidatePath := fs.String("candidate", "", "CandidateSpec JSON file")
	out := fs.String("out", "", "JSON report path")
	chunk := fs.Int("chunk", snapshot.DefaultChunk, "snapshot chunk size")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *sqlPath == "" || *candidatePath == "" || *out == "" {
		return fmt.Errorf("--sql, --candidate and --out are required")
	}
	sqlBytes, err := os.ReadFile(*sqlPath)
	if err != nil {
		return fmt.Errorf("read SQL file: %w", err)
	}
	f, err := os.Open(*candidatePath)
	if err != nil {
		return fmt.Errorf("open CandidateSpec: %w", err)
	}
	spec, decodeErr := candidate.DecodeStrict(f)
	closeErr := f.Close()
	if decodeErr != nil {
		return decodeErr
	}
	if closeErr != nil {
		return closeErr
	}
	prepared, err := shadowgate.Prepare(string(sqlBytes), spec)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	dbs, closeAll, err := openBoth(ctx)
	if err != nil {
		return err
	}
	defer closeAll()
	report, err := (shadowgate.Gate{Baseline: dbs["baseline"], Candidate: dbs["candidate"], Chunk: *chunk}).Run(ctx, prepared)
	if err != nil {
		return err
	}
	outFile, err := os.Create(*out)
	if err != nil {
		return fmt.Errorf("create EXPLAIN report: %w", err)
	}
	if err := shadowgate.WriteJSON(outFile, report); err != nil {
		outFile.Close()
		return fmt.Errorf("write EXPLAIN report: %w", err)
	}
	if err := outFile.Close(); err != nil {
		return err
	}
	fmt.Printf("candidate EXPLAIN report: %s (L1, performance claim eligible=false, index created=%t)\n", *out, report.Candidate.Created)
	return nil
}
