package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sqlsentinel/internal/candidate"
	"sqlsentinel/internal/pipeline"
	"sqlsentinel/internal/shadowgate"
	"sqlsentinel/internal/snapshot"
)

func runPipeline(args []string) error {
	fs := flag.NewFlagSet("pipeline", flag.ExitOnError)
	sqlPath := fs.String("sql", "", "single read-only SQL file")
	candidatePath := fs.String("candidate", "", "CandidateSpec JSON file")
	outDir := fs.String("out-dir", "", "new or empty artifact output directory")
	chunk := fs.Int("chunk", snapshot.DefaultChunk, "snapshot chunk size")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *sqlPath == "" || *candidatePath == "" || *outDir == "" {
		return fmt.Errorf("--sql, --candidate and --out-dir are required")
	}
	sqlRaw, err := os.ReadFile(*sqlPath)
	if err != nil {
		return fmt.Errorf("read SQL file: %w", err)
	}
	candidateRaw, err := os.ReadFile(*candidatePath)
	if err != nil {
		return fmt.Errorf("read CandidateSpec: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	report, err := pipeline.Run(ctx, pipeline.Input{
		SQL:           sqlRaw,
		CandidateSpec: candidateRaw,
		OutputDir:     *outDir,
		RunExplain: func(ctx context.Context, sql string, rawSpec []byte) (shadowgate.Report, []byte, error) {
			spec, err := candidate.DecodeStrict(bytes.NewReader(rawSpec))
			if err != nil {
				return shadowgate.Report{}, nil, err
			}
			prepared, err := shadowgate.Prepare(sql, spec)
			if err != nil {
				return shadowgate.Report{}, nil, err
			}
			dbs, closeAll, err := openBoth(ctx)
			if err != nil {
				return shadowgate.Report{}, nil, err
			}
			defer closeAll()
			candidateReport, err := (shadowgate.Gate{Baseline: dbs["baseline"], Candidate: dbs["candidate"], Chunk: *chunk}).Run(ctx, prepared)
			if err != nil {
				return shadowgate.Report{}, nil, err
			}
			var baselineExplain string
			if err := dbs["baseline"].QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+prepared.SQL).Scan(&baselineExplain); err != nil {
				return shadowgate.Report{}, nil, fmt.Errorf("explain baseline SQL: %w", err)
			}
			return candidateReport, []byte(baselineExplain), nil
		},
	})
	if err != nil {
		if report.StoppedStep != "" {
			codes := make([]string, len(report.RejectionReasons))
			for i, reason := range report.RejectionReasons {
				codes[i] = reason.Code
			}
			fmt.Fprintf(os.Stderr, "pipeline rejected: report=%s stopped_step=%s rejection_reasons=%s\n", filepath.Join(*outDir, pipeline.SummaryFile), report.StoppedStep, strings.Join(codes, ","))
		}
		return err
	}
	fmt.Printf("pipeline report: %s (completed=%v, stopped=%s, performance claim eligible=%t)\n", *outDir, report.CompletedSteps, report.StoppedStep, report.EligibleForPerformanceClaim)
	return nil
}
