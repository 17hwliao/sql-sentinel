// Command kafka-worker consumes SQL Sentinel job references and uses the
// existing MySQL lease/CAS workflow. The actual runner is deliberately
// injected by deployment code; this binary never treats a missing runner as
// a successful SQL verification.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	_ "github.com/go-sql-driver/mysql"
	segmentkafka "github.com/segmentio/kafka-go"

	workflow "sqlsentinel/internal/kafka"
)

type config struct {
	MySQLDSN    string
	Brokers     []string
	GroupID     string
	MaxAttempts int
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(os.Getenv, ctx); err != nil {
		log.Fatal(err)
	}
}

func run(lookup func(string) string, ctx context.Context) error {
	cfg, err := loadConfig(lookup)
	if err != nil {
		return err
	}
	db, err := openDB(ctx, cfg.MySQLDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	reader := segmentkafka.NewReader(segmentkafka.ReaderConfig{Brokers: cfg.Brokers, Topic: "sql-sentinel.jobs", GroupID: cfg.GroupID, MinBytes: 1, MaxBytes: 1 << 20})
	defer reader.Close()
	// A deployment must wire an isolated shadow runner before it is allowed to
	// complete jobs. Failing closed preserves SQL Sentinel's evidence boundary.
	worker := &workflow.Worker{Store: &workflow.MySQLStore{DB: db}, Source: workflow.KafkaReader{Reader: reader}, Config: workflow.Config{Owner: cfg.GroupID, MaxAttempts: cfg.MaxAttempts}, Runner: func(context.Context, workflow.Job) error { return workflow.ErrRejected }}
	log.Printf("sql sentinel worker started group_id=%s max_attempts=%d runner=controlled-refusal", cfg.GroupID, cfg.MaxAttempts)
	err = worker.Run(ctx)
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return nil
	}
	return err
}

func openDB(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping control plane: %w", err)
	}
	return db, nil
}
func loadConfig(lookup func(string) string) (config, error) {
	if lookup == nil {
		return config{}, errors.New("environment lookup is required")
	}
	dsn, raw := strings.TrimSpace(lookup("SQL_SENTINEL_KAFKA_MYSQL_DSN")), strings.TrimSpace(lookup("SQL_SENTINEL_KAFKA_BROKERS"))
	if dsn == "" || raw == "" {
		return config{}, errors.New("SQL_SENTINEL_KAFKA_MYSQL_DSN and SQL_SENTINEL_KAFKA_BROKERS are required")
	}
	cfg := config{MySQLDSN: dsn, GroupID: "sql-sentinel-worker-v1", MaxAttempts: 3}
	if value := strings.TrimSpace(lookup("SQL_SENTINEL_KAFKA_WORKER_GROUP")); value != "" {
		cfg.GroupID = value
	}
	if value := strings.TrimSpace(lookup("SQL_SENTINEL_KAFKA_WORKER_MAX_ATTEMPTS")); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return config{}, errors.New("SQL_SENTINEL_KAFKA_WORKER_MAX_ATTEMPTS must be positive")
		}
		cfg.MaxAttempts = n
	}
	for _, b := range strings.Split(raw, ",") {
		if b = strings.TrimSpace(b); b != "" {
			cfg.Brokers = append(cfg.Brokers, b)
		}
	}
	if len(cfg.Brokers) == 0 {
		return config{}, errors.New("SQL_SENTINEL_KAFKA_BROKERS must contain a broker")
	}
	return cfg, nil
}
