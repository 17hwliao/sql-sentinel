package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sqlsentinel/internal/configenv"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"sqlsentinel/internal/agentgraph"
	"sqlsentinel/internal/historicalprior"
	"sqlsentinel/internal/identity"
	workflow "sqlsentinel/internal/kafka"
	"sqlsentinel/internal/rag"
	"sqlsentinel/internal/service"
	"sqlsentinel/internal/telemetry"
)

func main() {
	if err := configenv.LoadSecretFiles(); err != nil {
		log.Fatal("secret_configuration_failed")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal("sentinel_api_failed")
	}
}
func run(ctx context.Context) error {
	token := os.Getenv("SQL_SENTINEL_API_TOKEN")
	if len(token) < 32 {
		return errors.New("api_token_too_short")
	}
	dsn := os.Getenv("SQL_SENTINEL_KAFKA_MYSQL_DSN")
	if dsn == "" {
		return errors.New("control_dsn_required")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(12)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err = db.PingContext(ctx); err != nil {
		return err
	}
	store := &workflow.MySQLStore{DB: db}
	var history *historicalprior.Client
	if endpoint := os.Getenv("SQL_SENTINEL_ES_URL"); endpoint != "" {
		history, err = historicalprior.New(endpoint, os.Getenv("SQL_SENTINEL_ES_API_KEY"), os.Getenv("SQL_SENTINEL_ES_CA"))
		if err != nil {
			return errors.New("history_configuration_invalid")
		}
		go func() {
			tick := time.NewTicker(10 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					step, cancel := context.WithTimeout(ctx, 15*time.Second)
					if historicalprior.Relay(step, db, history) != nil {
						log.Print("history_relay_failed")
					}
					cancel()
				}
			}
		}()
	}
	auth, err := identity.New(db, []byte(os.Getenv("SQL_SENTINEL_JWT_KEY")), token)
	if err != nil {
		return err
	}
	tables, err := service.AllowedTables(os.Getenv("SQL_SENTINEL_SHADOW_TABLES"))
	if err != nil {
		return err
	}
	var knowledge *rag.Service
	var answerModel agentgraph.DraftNode
	if endpoint := os.Getenv("SQL_SENTINEL_RAG_EMBEDDING_URL"); endpoint != "" {
		dim, e := strconv.Atoi(os.Getenv("SQL_SENTINEL_RAG_DIMENSION"))
		if e != nil || dim < 1 || dim > 4096 {
			return errors.New("rag_dimension_invalid")
		}
		embed, e := rag.NewHTTPEmbeddingProvider(rag.HTTPEmbeddingConfig{Endpoint: endpoint, APIKey: os.Getenv("OPENAI_API_KEY"), Model: "embedding", Dimension: dim})
		if e != nil {
			return e
		}
		knowledge, e = rag.NewService(embed, &rag.MySQLVectorStore{DB: db, Dim: dim}, rag.ChunkConfig{MaxRunes: 400, OverlapRunes: 50})
		if e != nil {
			return e
		}
		answerModel, e = agentgraph.NewOpenAINode(ctx, os.Getenv("OPENAI_API_KEY"), os.Getenv("EINO_MODEL"), os.Getenv("EINO_BASE_URL"))
		if e != nil {
			return e
		}
	}
	metrics, err := telemetry.New(ctx, "sql-sentinel", os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	if err != nil {
		return err
	}
	metrics.WatchSQL(ctx, db, "backend_sql_expired_jobs", "Expired RUNNING job leases; -1 means store unavailable.", "SELECT COUNT(*) FROM sql_sentinel_jobs WHERE status='RUNNING' AND lease_until<UTC_TIMESTAMP(6)")
	metrics.WatchSQL(ctx, db, "backend_sql_pending_outbox", "Unpublished SQL events; -1 means store unavailable.", "SELECT COUNT(*) FROM sql_sentinel_outbox WHERE published_at IS NULL")
	if history != nil {
		metrics.WatchSQL(ctx, db, "backend_prior_pending_outbox", "Unpublished historical documents; -1 means store unavailable.", "SELECT COUNT(*) FROM sql_sentinel_prior_outbox WHERE published_at IS NULL")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = metrics.Close(cleanup)
	}()
	var gh *service.GitHub
	if secret := os.Getenv("GITHUB_WEBHOOK_SECRET"); secret != "" {
		if len(secret) < 32 {
			return errors.New("webhook_secret_too_short")
		}
		repositories := map[string]bool{}
		for _, repo := range strings.Split(os.Getenv("GITHUB_REPOSITORIES"), ",") {
			if repo = strings.TrimSpace(repo); repo != "" {
				repositories[repo] = true
			}
		}
		if len(repositories) == 0 {
			return errors.New("github_allowlist_required")
		}
		gh = &service.GitHub{Secret: secret, Token: os.Getenv("GITHUB_TOKEN"), Repositories: repositories, PublishComments: os.Getenv("GITHUB_PUBLISH_COMMENTS") == "true"}
		if os.Getenv("SQL_SENTINEL_TEST_FIXTURES") == "true" {
			gh.BaseURL = "http://fixtures:8090"
		}
		if gh.PublishComments {
			if gh.Token == "" {
				return errors.New("github_token_required")
			}
			go gh.RunComments(ctx, db)
		}
	}
	addr := os.Getenv("SQL_SENTINEL_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	app := (&service.Server{Store: store, Token: token, GitHub: gh, Auth: auth, RAG: knowledge, AnswerModel: answerModel, AllowedTables: tables, History: history}).Handler()
	root := http.NewServeMux()
	root.Handle("/metrics", metrics.Metrics())
	root.Handle("/", metrics.Wrap(app))
	server := &http.Server{Addr: addr, Handler: root, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
	}()
	err = server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-done
	return nil
}
