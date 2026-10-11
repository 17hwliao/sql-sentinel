package main

import (
	"context"
	"database/sql"
	_ "github.com/go-sql-driver/mysql"
	"log"
	"net"
	"os"
	"os/signal"
	"sqlsentinel/internal/configenv"
	workflow "sqlsentinel/internal/kafka"
	"sqlsentinel/internal/securegrpc"
	"sqlsentinel/internal/service"
	"sqlsentinel/internal/telemetry"
	"sqlsentinel/internal/verifierrpc"
	"syscall"
	"time"
)

func main() {
	if configenv.LoadSecretFiles() != nil {
		log.Fatal("secret_configuration_failed")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, e := sql.Open("mysql", os.Getenv("SQL_SENTINEL_KAFKA_MYSQL_DSN"))
	if e != nil {
		log.Fatal("control_configuration_invalid")
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	runner, cleanup, e := service.OpenRunner(ctx, os.Getenv, db)
	if e != nil {
		log.Fatal("runner_configuration_invalid")
	}
	defer cleanup()
	metrics, e := telemetry.New(ctx, "sentinel-verifier", os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	if e != nil {
		log.Fatal("telemetry_invalid")
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = metrics.Close(c)
	}()
	server, e := securegrpc.Server(securegrpc.Env(os.Getenv, "SQL_SENTINEL_GRPC"))
	if e != nil {
		log.Fatal("grpc_configuration_invalid")
	}
	store := &workflow.MySQLStore{DB: db}
	verifierrpc.RegisterVerifierServer(server, verifierrpc.NewServer(store.GetByID, runner.Run))
	address := os.Getenv("SQL_SENTINEL_VERIFIER_LISTEN")
	if address == "" {
		address = "127.0.0.1:50052"
	}
	listener, e := net.Listen("tcp", address)
	if e != nil {
		log.Fatal("listen_failed")
	}
	go func() {
		<-ctx.Done()
		done := make(chan struct{})
		go func() { server.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			server.Stop()
		}
	}()
	if e = server.Serve(listener); e != nil && ctx.Err() == nil {
		log.Fatal("grpc_serve_failed")
	}
}
