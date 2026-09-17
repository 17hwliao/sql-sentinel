// Command kafka-relay continuously publishes SQL Sentinel's durable outbox.
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
	"time"

	_ "github.com/go-sql-driver/mysql"
	segmentkafka "github.com/segmentio/kafka-go"

	workflow "sqlsentinel/internal/kafka"
)

type config struct {
	MySQLDSN  string
	Brokers   []string
	Interval  time.Duration
	BatchSize int
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
	writer := segmentkafka.NewWriter(segmentkafka.WriterConfig{Brokers: cfg.Brokers, Balancer: &segmentkafka.Hash{}})
	defer writer.Close()
	relay := workflow.Relay{Store: &workflow.MySQLStore{DB: db}, Publisher: workflow.KafkaWriter{Writer: writer}, BatchSize: cfg.BatchSize}
	log.Printf("sql sentinel relay started interval=%s batch_size=%d", cfg.Interval, cfg.BatchSize)
	return workflow.RunPolling(ctx, cfg.Interval, relay.RunOnce, func(result workflow.PollResult) {
		if result.Err != nil {
			log.Printf("sql sentinel relay scan failed published=%d error=%v", result.Published, result.Err)
		} else if result.Published > 0 {
			log.Printf("sql sentinel relay published=%d", result.Published)
		}
	})
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
	cfg := config{MySQLDSN: dsn, Interval: time.Second, BatchSize: 16}
	if value := strings.TrimSpace(lookup("SQL_SENTINEL_KAFKA_RELAY_INTERVAL")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return config{}, errors.New("SQL_SENTINEL_KAFKA_RELAY_INTERVAL must be positive")
		}
		cfg.Interval = parsed
	}
	if value := strings.TrimSpace(lookup("SQL_SENTINEL_KAFKA_RELAY_BATCH_SIZE")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			return config{}, errors.New("SQL_SENTINEL_KAFKA_RELAY_BATCH_SIZE must be positive")
		}
		cfg.BatchSize = parsed
	}
	for _, broker := range strings.Split(raw, ",") {
		if broker = strings.TrimSpace(broker); broker != "" {
			cfg.Brokers = append(cfg.Brokers, broker)
		}
	}
	if len(cfg.Brokers) == 0 {
		return config{}, errors.New("SQL_SENTINEL_KAFKA_BROKERS must contain a broker")
	}
	return cfg, nil
}
