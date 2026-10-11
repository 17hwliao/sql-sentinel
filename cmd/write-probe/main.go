package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	_ "github.com/go-sql-driver/mysql"
	"os"
	"sqlsentinel/internal/candidate"
	"sqlsentinel/internal/configenv"
	"sqlsentinel/internal/writebench"
	"time"
)

func main() {
	specFile := flag.String("spec-file", "", "index spec")
	out := flag.String("out", "", "report path")
	ack := flag.Bool("ack-isolated", false, "acknowledge synthetic isolated fixture")
	flag.Parse()
	if !*ack || *specFile == "" || *out == "" || configenv.LoadSecretFiles() != nil {
		fail()
	}
	f, e := os.Open(*specFile)
	if e != nil {
		fail()
	}
	s, e := candidate.DecodeStrict(f)
	f.Close()
	if e != nil || writebench.Validate(s) != nil {
		fail()
	}
	db, e := sql.Open("mysql", os.Getenv("SQL_SENTINEL_WRITE_DSN"))
	if e != nil {
		fail()
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r, e := writebench.Run(ctx, db, s)
	if e != nil {
		fail()
	}
	raw, e := json.MarshalIndent(r, "", "  ")
	if e != nil || os.WriteFile(*out, raw, 0600) != nil {
		fail()
	}
}
func fail() { os.Stderr.WriteString("isolated_write_probe_failed\n"); os.Exit(1) }
