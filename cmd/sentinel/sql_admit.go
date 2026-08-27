package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"sqlsentinel/internal/sqladmit"
)

func runSQLAdmit(args []string) error {
	fs := flag.NewFlagSet("sql-admit", flag.ExitOnError)
	in := fs.String("in", "", "single read-only SQL file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return fmt.Errorf("--in is required")
	}
	b, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(sqladmit.Admit(string(b)))
}
