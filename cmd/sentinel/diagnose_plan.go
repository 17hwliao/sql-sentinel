package main

import (
	"flag"
	"fmt"
	"os"

	"sqlsentinel/internal/diagnosis"
)

func runDiagnosePlan(args []string) error {
	fs := flag.NewFlagSet("diagnose-plan", flag.ExitOnError)
	in := fs.String("in", "", "L1 EXPLAIN comparison JSON path")
	out := fs.String("out", "", "diagnostic hypotheses JSON path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return fmt.Errorf("--in and --out are required")
	}
	raw, err := os.ReadFile(*in)
	if err != nil {
		return fmt.Errorf("read EXPLAIN comparison: %w", err)
	}
	report, err := diagnosis.Analyze(raw)
	if err != nil {
		return err
	}
	f, err := os.Create(*out)
	if err != nil {
		return fmt.Errorf("create diagnosis report: %w", err)
	}
	if err := diagnosis.WriteJSON(f, report); err != nil {
		f.Close()
		return fmt.Errorf("write diagnosis report: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("diagnostic hypotheses: %s (L1, performance claim eligible=false, hypotheses=%d)\n", *out, len(report.Hypotheses))
	return nil
}
