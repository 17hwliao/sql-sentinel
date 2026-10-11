package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"os"
	"sqlsentinel/internal/configenv"
	"sqlsentinel/internal/crossengine"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "crossengine_comparison_failed")
		os.Exit(1)
	}
}
func run() error {
	if e := configenv.LoadSecretFiles(); e != nil {
		return e
	}
	path := flag.String("sql-file", "", "one portable admitted read-only SQL query")
	out := flag.String("out", "", "report destination")
	ack := flag.Bool("ack-isolated", false, "confirm source is an isolated MySQL shadow and target is sentinel_compare")
	flag.Parse()
	if !*ack || *path == "" || *out == "" {
		return crossengine.ErrUnsupported
	}
	raw, e := os.ReadFile(*path)
	if e != nil || len(raw) > 64<<10 {
		return crossengine.ErrUnsupported
	}
	source, e := sql.Open("mysql", os.Getenv("SQL_SENTINEL_PG_MYSQL_DSN"))
	if e != nil {
		return e
	}
	defer source.Close()
	source.SetMaxOpenConns(2)
	target, e := sql.Open("pgx", os.Getenv("SQL_SENTINEL_POSTGRES_DSN"))
	if e != nil {
		return e
	}
	defer target.Close()
	target.SetMaxOpenConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	report, e := crossengine.Run(ctx, source, target, string(raw))
	if e != nil {
		return e
	}
	data, e := json.MarshalIndent(report, "", "  ")
	if e != nil {
		return e
	}
	if e = os.WriteFile(*out, append(data, '\n'), 0600); e != nil {
		return e
	}
	fmt.Println("crossengine_report_written")
	return nil
}
