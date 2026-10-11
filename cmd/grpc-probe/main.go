package main

import (
	"context"
	"flag"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"os"
	"sqlsentinel/internal/configenv"
	"sqlsentinel/internal/securegrpc"
	"time"
)

func main() {
	target := flag.String("target", "", "private gRPC target")
	prefix := flag.String("prefix", "AGENTMESH_GRPC", "TLS configuration prefix")
	flag.Parse()
	if configenv.LoadSecretFiles() != nil {
		os.Exit(1)
	}
	conn, e := securegrpc.Dial(*target, securegrpc.Env(os.Getenv, *prefix))
	if e != nil {
		os.Exit(1)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, e := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if e != nil || r.Status != healthpb.HealthCheckResponse_SERVING {
		os.Exit(1)
	}
}
