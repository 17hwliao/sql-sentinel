package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"sqlsentinel/internal/candidate"
	"sqlsentinel/internal/plancompare"
	"sqlsentinel/internal/shadowgate"
	"sqlsentinel/internal/snapshot"
)

func runExplainCompare(args []string) error {
	fs := flag.NewFlagSet("explain-compare", flag.ExitOnError)
	sqlPath := fs.String("sql", "", "admitted single read-only SQL file")
	candidatePath := fs.String("candidate", "", "CandidateSpec JSON file")
	out := fs.String("out", "", "JSON comparison report path")
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
	candidateReport, err := (shadowgate.Gate{Baseline: dbs["baseline"], Candidate: dbs["candidate"], Chunk: *chunk}).Run(ctx, prepared)
	if err != nil {
		return err
	}
	var baselineExplain string
	if err := dbs["baseline"].QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+prepared.SQL).Scan(&baselineExplain); err != nil {
		return fmt.Errorf("explain baseline SQL: %w", err)
	}
	report, err := plancompare.BuildReport(plancompare.Metadata{
		GeneratedAt:  candidateReport.GeneratedAt,
		MySQLVersion: candidateReport.MySQLVersion,
		Snapshot:     plancompare.Snapshot{Digest: candidateReport.Snapshot.Digest, Rows: candidateReport.Snapshot.Rows},
		SQL:          plancompare.SQLInput{SQL: candidateReport.SQL.SQL, Signals: candidateReport.SQL.Signals},
		CandidateIndex: plancompare.CandidateIndex{
			Table: candidateReport.Candidate.Table, Name: candidateReport.Candidate.Name,
			DDL: candidateReport.Candidate.DDL, Created: candidateReport.Candidate.Created,
		},
	}, []byte(baselineExplain), candidateReport.ExplainJSON)
	if err != nil {
		return err
	}
	outFile, err := os.Create(*out)
	if err != nil {
		return fmt.Errorf("create comparison report: %w", err)
	}
	if err := plancompare.WriteJSON(outFile, report); err != nil {
		outFile.Close()
		return fmt.Errorf("write comparison report: %w", err)
	}
	if err := outFile.Close(); err != nil {
		return err
	}
	fmt.Printf("EXPLAIN comparison report: %s (L1, performance claim eligible=false)\n", *out)
	return nil
}
