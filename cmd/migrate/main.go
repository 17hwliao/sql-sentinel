package main

import (
	"context"
	"database/sql"
	_ "github.com/go-sql-driver/mysql"
	"log"
	"os"
	"sqlsentinel/internal/configenv"
	workflow "sqlsentinel/internal/kafka"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Fatal("migration_failed")
	}
	log.Print("migration_complete")
}
func run() error {
	if err := configenv.LoadSecretFiles(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := sql.Open("mysql", os.Getenv("SQL_SENTINEL_MIGRATE_MYSQL_DSN"))
	if err != nil {
		return err
	}
	defer db.Close()
	return (&workflow.MySQLStore{DB: db}).EnsureSchema(ctx)
}
