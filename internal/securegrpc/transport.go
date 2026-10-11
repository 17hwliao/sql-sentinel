// Package securegrpc provides mandatory mTLS plus independent service tokens.
package securegrpc

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"os"
	"time"
)

type Config struct{ CA, Cert, Key, ServerName, Token string }

func TLS(c Config, server bool) (*tls.Config, error) {
	if c.CA == "" || c.Cert == "" || c.Key == "" || len(c.Token) < 32 || len(c.Token) > 256 || (!server && c.ServerName == "") {
		return nil, errors.New("grpc_configuration_invalid")
	}
	cert, e := tls.LoadX509KeyPair(c.Cert, c.Key)
	if e != nil {
		return nil, errors.New("grpc_certificate_invalid")
	}
	raw, e := os.ReadFile(c.CA)
	if e != nil {
		return nil, errors.New("grpc_ca_invalid")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		return nil, errors.New("grpc_ca_invalid")
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: roots, ServerName: c.ServerName}
	if server {
		cfg.ClientCAs = roots
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}
func authorized(ctx context.Context, token string) bool {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 {
		return false
	}
	actual := sha256.Sum256([]byte(values[0]))
	expected := sha256.Sum256([]byte("Bearer " + token))
	return subtle.ConstantTimeCompare(actual[:], expected[:]) == 1
}
func Server(c Config) (*grpc.Server, error) {
	tlsCfg, e := TLS(c, true)
	if e != nil {
		return nil, e
	}
	unary := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !authorized(ctx, c.Token) {
			return nil, status.Error(codes.Unauthenticated, "service_auth_failed")
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Minute {
			return nil, status.Error(codes.InvalidArgument, "deadline_required")
		}
		ctx = extract(ctx)
		ctx, span := otel.Tracer("grpc.server").Start(ctx, "grpc.unary")
		defer span.End()
		return handler(ctx, req)
	}
	stream := func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !authorized(ss.Context(), c.Token) {
			return status.Error(codes.Unauthenticated, "service_auth_failed")
		}
		deadline, ok := ss.Context().Deadline()
		if !ok || time.Until(deadline) > 3*time.Minute {
			return status.Error(codes.InvalidArgument, "bounded_deadline_required")
		}
		ctx, span := otel.Tracer("grpc.server").Start(extract(ss.Context()), "grpc.stream")
		defer span.End()
		return handler(srv, contextStream{ServerStream: ss, ctx: ctx})
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)), grpc.MaxRecvMsgSize(128<<10), grpc.MaxSendMsgSize(128<<10), grpc.MaxConcurrentStreams(32), grpc.UnaryInterceptor(unary), grpc.StreamInterceptor(stream))
	h := health.NewServer()
	h.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(server, h)
	return server, nil
}

type contextStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s contextStream) Context() context.Context { return s.ctx }
func extract(ctx context.Context) context.Context {
	md, _ := metadata.FromIncomingContext(ctx)
	carrier := propagation.MapCarrier{}
	for _, key := range []string{"traceparent", "tracestate"} {
		if values := md.Get(key); len(values) == 1 {
			carrier[key] = values[0]
		}
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

type tokenCredential struct{ value string }

func (t tokenCredential) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	carrier := propagation.MapCarrier{"authorization": "Bearer " + t.value}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return map[string]string(carrier), nil
}
func (tokenCredential) RequireTransportSecurity() bool { return true }
func Dial(target string, c Config) (*grpc.ClientConn, error) {
	tlsCfg, e := TLS(c, false)
	if e != nil {
		return nil, e
	}
	return grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)), grpc.WithPerRPCCredentials(tokenCredential{c.Token}), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(128<<10), grpc.MaxCallSendMsgSize(128<<10)))
}
func Env(lookup func(string) string, prefix string) Config {
	return Config{CA: lookup(prefix + "_CA"), Cert: lookup(prefix + "_CERT"), Key: lookup(prefix + "_KEY"), ServerName: lookup(prefix + "_SERVER_NAME"), Token: lookup(prefix + "_TOKEN")}
}
