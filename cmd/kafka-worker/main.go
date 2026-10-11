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
	"sqlsentinel/internal/configenv"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
	segmentkafka "github.com/segmentio/kafka-go"

	workflow "sqlsentinel/internal/kafka"
	"sqlsentinel/internal/securegrpc"
	"sqlsentinel/internal/service"
	"sqlsentinel/internal/telemetry"
	"sqlsentinel/internal/verifierrpc"
)

type config struct {
	MySQLDSN    string
	Brokers     []string
	GroupID     string
	MaxAttempts int
	Lease       time.Duration
}

func main() {
	if err := configenv.LoadSecretFiles(); err != nil {
		log.Fatal("secret_configuration_failed")
	}
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
	metrics, err := telemetry.New(ctx, "sentinel-worker", lookup("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = metrics.Close(cleanup)
	}()
	reader := segmentkafka.NewReader(segmentkafka.ReaderConfig{Brokers: cfg.Brokers, Topic: "sql-sentinel.jobs", GroupID: cfg.GroupID, MinBytes: 1, MaxBytes: 1 << 20})
	defer reader.Close()
	var runJob workflow.Runner
	if target := lookup("SQL_SENTINEL_VERIFIER_GRPC_TARGET"); target != "" {
		conn, err := securegrpc.Dial(target, securegrpc.Env(lookup, "SQL_SENTINEL_GRPC"))
		if err != nil {
			return errors.New("verifier_transport_invalid")
		}
		defer conn.Close()
		runJob = verifierrpc.Client(conn, nil)
	} else {
		runner, cleanup, err := service.OpenRunner(ctx, lookup, db)
		if err != nil {
			return err
		}
		defer cleanup()
		runJob = runner.Run
	}
	worker := &workflow.Worker{Store: &workflow.MySQLStore{DB: db}, Source: workflow.KafkaReader{Reader: reader}, Config: workflow.Config{Owner: cfg.GroupID, MaxAttempts: cfg.MaxAttempts, Lease: cfg.Lease}, Runner: runJob}
	log.Printf("sql sentinel worker started max_attempts=%d runner=isolated-shadow", cfg.MaxAttempts)
	err = worker.Run(ctx)
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return nil
	}
	return err
}

func openDB(ctx context.Context, dsn string) (*sql.DB, error) {
	if dsn == "" {
		return nil, errors.New("database_configuration_missing")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(6)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
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
	cfg := config{MySQLDSN: dsn, GroupID: "sql-sentinel-worker-v1", MaxAttempts: 3, Lease: 3 * time.Minute}
	if value := lookup("SQL_SENTINEL_WORKER_LEASE"); value != "" {
		lease, err := time.ParseDuration(value)
		if err != nil || lease < 5*time.Second || lease > time.Hour {
			return config{}, errors.New("worker_lease_invalid")
		}
		cfg.Lease = lease
	}
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
