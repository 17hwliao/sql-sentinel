package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	segmentkafka "github.com/segmentio/kafka-go"

	"sqlsentinel/internal/candidate"
	workflow "sqlsentinel/internal/kafka"
	"sqlsentinel/internal/pipeline"
	"sqlsentinel/internal/snapshot"
	"sqlsentinel/internal/webhook"
)

const (
	workflowMySQLEnvironment   = "SQL_SENTINEL_KAFKA_MYSQL_DSN"
	workflowBrokersEnvironment = "SQL_SENTINEL_KAFKA_BROKERS"
	workflowControlTokenEnv    = "SQL_SENTINEL_CONTROL_TOKEN"
)

// kafka-serve is deliberately a local reference process. It assembles the
// durable workflow components without copying SQL or CandidateSpec into Kafka.
func runKafkaServe(args []string) error {
	fs := flag.NewFlagSet("kafka-serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:18081", "loopback address for the Kafka workflow service")
	candidatePath := fs.String("candidate", "", "local CandidateSpec JSON file")
	outputRoot := fs.String("out-dir", "", "worker artifact root")
	mysqlDSN := fs.String("mysql-dsn", strings.TrimSpace(os.Getenv(workflowMySQLEnvironment)), "Kafka control-plane MySQL DSN")
	brokersRaw := fs.String("brokers", strings.TrimSpace(os.Getenv(workflowBrokersEnvironment)), "comma-separated Kafka brokers")
	groupID := fs.String("group", "sql-sentinel-worker-v1", "Kafka consumer group")
	maxConcurrent := fs.Int("max-concurrent", 1, "maximum concurrent webhook admissions (1-4)")
	chunk := fs.Int("chunk", snapshot.DefaultChunk, "snapshot chunk size")
	pollInterval := fs.Duration("relay-interval", time.Second, "outbox relay scan interval")
	maxAttempts := fs.Int("max-attempts", 3, "maximum worker attempts before dead-lettering")
	retryBase := fs.Duration("retry-base", time.Second, "base retry delay")
	lease := fs.Duration("lease", 5*time.Minute, "worker lease duration")
	leaseRenewEvery := fs.Duration("lease-renew-every", 0, "worker lease renewal interval; defaults to one third of --lease")
	runTimeout := fs.Duration("run-timeout", 15*time.Minute, "maximum duration for one claimed workflow")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateWebhookListen(*listen); err != nil {
		return err
	}
	if *candidatePath == "" || *outputRoot == "" || *mysqlDSN == "" || *brokersRaw == "" {
		return errors.New("--candidate, --out-dir, --mysql-dsn, and --brokers are required")
	}
	if *maxConcurrent < 1 || *maxConcurrent > 4 || *chunk < 1 || *pollInterval <= 0 || *maxAttempts < 1 || *retryBase <= 0 || *lease <= 0 || *runTimeout <= 0 || strings.TrimSpace(*groupID) == "" {
		return errors.New("invalid Kafka workflow server configuration")
	}
	secret := os.Getenv("SQL_SENTINEL_WEBHOOK_SECRET")
	if secret == "" {
		return errors.New("SQL_SENTINEL_WEBHOOK_SECRET is required")
	}
	controlToken := strings.TrimSpace(os.Getenv(workflowControlTokenEnv))
	if controlToken == "" {
		return fmt.Errorf("%s is required", workflowControlTokenEnv)
	}
	candidateRaw, err := os.ReadFile(*candidatePath)
	if err != nil {
		return fmt.Errorf("read CandidateSpec: %w", err)
	}
	spec, err := candidate.DecodeStrict(bytes.NewReader(candidateRaw))
	if err != nil {
		return err
	}
	if err := candidate.Validate(spec); err != nil {
		return err
	}
	brokers := splitBrokers(*brokersRaw)
	if len(brokers) == 0 {
		return errors.New("--brokers must contain at least one broker")
	}

	db, err := sql.Open("mysql", *mysqlDSN)
	if err != nil {
		return fmt.Errorf("open Kafka control-plane MySQL: %w", err)
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping Kafka control-plane MySQL: %w", err)
	}
	store := &workflow.MySQLStore{DB: db}
	if err := store.EnsureSchema(ctx); err != nil {
		return err
	}

	writer := segmentkafka.NewWriter(segmentkafka.WriterConfig{Brokers: brokers, Balancer: &segmentkafka.Hash{}})
	defer writer.Close()
	reader := segmentkafka.NewReader(segmentkafka.ReaderConfig{Brokers: brokers, GroupID: *groupID, Topic: "sql-sentinel.jobs", MinBytes: 1, MaxBytes: 10e6})
	defer reader.Close()

	server, err := webhook.New(webhook.Config{
		Secret: []byte(secret), OutputRoot: *outputRoot, MaxConcurrent: *maxConcurrent,
		CandidateSpec: candidateRaw,
		AsyncEnqueue:  store.Receive,
	})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle(webhook.Path, server)
	mux.Handle("/jobs/", workflow.JobHandler(store, controlToken))
	mux.Handle("/dead-letters", workflow.ControlHandler(store, controlToken))
	httpServer := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second}

	relay := workflow.Relay{Store: store, Publisher: workflow.KafkaWriter{Writer: writer}, BatchSize: 16}
	worker := &workflow.Worker{
		Store: store, Source: workflow.KafkaReader{Reader: reader},
		Runner: workflowPipelineRunner(*outputRoot, *chunk),
		Config: workflow.Config{Owner: workflowOwner(), MaxAttempts: *maxAttempts, RetryBase: *retryBase, Lease: *lease, LeaseRenewEvery: *leaseRenewEvery, RunTimeout: *runTimeout},
	}
	errs := make(chan error, 3)
	go func() { errs <- runWorkflowRelay(ctx, relay, *pollInterval) }()
	go func() { errs <- worker.Run(ctx) }()
	go func() {
		fmt.Printf("Kafka workflow service listening on http://%s%s; job status at /jobs/{delivery_id}; protected dead-letter control at /dead-letters\n", *listen, webhook.Path)
		err := httpServer.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			errs <- nil
			return
		}
		errs <- err
	}()

	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
		return nil
	case err := <-errs:
		stop()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
		return err
	}
}

func workflowPipelineRunner(root string, chunk int) workflow.Runner {
	return func(ctx context.Context, job workflow.Job) error {
		attemptDir := filepath.Join(root, "jobs", job.ID, fmt.Sprintf("attempt-%d", job.Attempts))
		_, err := pipeline.Run(ctx, pipeline.Input{
			SQL: job.SQL, CandidateSpec: job.CandidateSpec, OutputDir: attemptDir,
			RunExplain: pipelineExplainRunner(chunk),
		})
		return err
	}
}

func runWorkflowRelay(ctx context.Context, relay workflow.Relay, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		published, err := relay.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("Kafka workflow relay scan failed published=%d error=%v", published, err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func splitBrokers(raw string) []string {
	var brokers []string
	for _, broker := range strings.Split(raw, ",") {
		if broker = strings.TrimSpace(broker); broker != "" {
			brokers = append(brokers, broker)
		}
	}
	return brokers
}

func workflowOwner() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "local"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}
