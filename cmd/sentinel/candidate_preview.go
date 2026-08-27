package main

import (
	"flag"
	"fmt"
	"os"

	"sqlsentinel/internal/candidate"
)

// candidate-preview deliberately only reads a file and writes stdout.
// It has no database handle and therefore cannot execute the generated DDL.
func runCandidatePreview(args []string) error {
	fs := flag.NewFlagSet("candidate-preview", flag.ExitOnError)
	in := fs.String("in", "", "CandidateSpec JSON path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return fmt.Errorf("--in is required")
	}
	f, err := os.Open(*in)
	if err != nil {
		return fmt.Errorf("open CandidateSpec %q: %w", *in, err)
	}
	defer f.Close()
	spec, err := candidate.DecodeStrict(f)
	if err != nil {
		return err
	}
	ddl, err := candidate.CompileCreateIndex(spec)
	if err != nil {
		return err
	}
	fmt.Println(ddl)
	return nil
}
