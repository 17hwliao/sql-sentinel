package main

import (
	"flag"
	"fmt"
	"os"

	"sqlsentinel/internal/evidencebind"
)

func runBindEvidence(args []string) error {
	fs := flag.NewFlagSet("bind-evidence", flag.ExitOnError)
	diagnosisPath := fs.String("diagnosis", "", "diagnostic hypotheses JSON path")
	comparisonPath := fs.String("comparison", "", "EXPLAIN comparison JSON path")
	measurementPath := fs.String("measurement", "", "bench measurement JSON path")
	seriesPath := fs.String("series", "", "series admission JSON path")
	out := fs.String("out", "", "evidence binding JSON path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *diagnosisPath == "" || *comparisonPath == "" || *measurementPath == "" || *seriesPath == "" || *out == "" {
		return fmt.Errorf("--diagnosis, --comparison, --measurement, --series and --out are required")
	}
	diagnosisRaw, err := os.ReadFile(*diagnosisPath)
	if err != nil {
		return fmt.Errorf("read diagnosis: %w", err)
	}
	comparisonRaw, err := os.ReadFile(*comparisonPath)
	if err != nil {
		return fmt.Errorf("read EXPLAIN comparison: %w", err)
	}
	measurementRaw, err := os.ReadFile(*measurementPath)
	if err != nil {
		return fmt.Errorf("read measurement: %w", err)
	}
	seriesRaw, err := os.ReadFile(*seriesPath)
	if err != nil {
		return fmt.Errorf("read series admission: %w", err)
	}
	report, err := evidencebind.Analyze(diagnosisRaw, comparisonRaw, measurementRaw, seriesRaw, *measurementPath)
	if err != nil {
		return err
	}
	f, err := os.Create(*out)
	if err != nil {
		return fmt.Errorf("create evidence binding: %w", err)
	}
	if err := evidencebind.WriteJSON(f, report); err != nil {
		f.Close()
		return fmt.Errorf("write evidence binding: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("evidence binding: %s (binding complete=%t, performance claim eligible=%t)\n", *out, report.BindingComplete, report.EligibleForPerformanceClaim)
	return nil
}
