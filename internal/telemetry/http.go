// Package telemetry provides bounded-cardinality metrics and safe HTTP spans.
package telemetry

import (
	"context"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdk "go.opentelemetry.io/otel/sdk/trace"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Runtime struct {
	Registry *prometheus.Registry
	Requests *prometheus.CounterVec
	Duration *prometheus.HistogramVec
	Active   prometheus.Gauge
	provider *sdk.TracerProvider
	Service  string
}

func New(ctx context.Context, service, endpoint string) (*Runtime, error) {
	r := &Runtime{Registry: prometheus.NewRegistry(), Service: service}
	r.Requests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "backend_http_requests_total", Help: "Requests by bounded endpoint, method and status."}, []string{"service", "endpoint", "method", "status"})
	r.Duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "backend_http_duration_seconds", Help: "Wall time including streaming, in seconds.", Buckets: []float64{.01, .05, .1, .5, 1, 5, 15, 60, 120}}, []string{"service", "endpoint"})
	r.Active = prometheus.NewGauge(prometheus.GaugeOpts{Name: "backend_http_active_requests", Help: "Currently active HTTP requests."})
	r.Registry.MustRegister(r.Requests, r.Duration, r.Active, prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	opts := []sdk.TracerProviderOption{sdk.WithResource(resource.NewSchemaless(attribute.String("service.name", service))), sdk.WithSampler(sdk.ParentBased(sdk.TraceIDRatioBased(.1)))}
	if endpoint != "" {
		e, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint), otlptracehttp.WithTimeout(3*time.Second))
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdk.WithBatcher(e, sdk.WithMaxQueueSize(256), sdk.WithExportTimeout(3*time.Second)))
	}
	r.provider = sdk.NewTracerProvider(opts...)
	otel.SetTracerProvider(r.provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return r, nil
}
func (r *Runtime) Close(ctx context.Context) error { return r.provider.Shutdown(ctx) }
func (r *Runtime) Metrics() http.Handler {
	return promhttp.HandlerFor(r.Registry, promhttp.HandlerOpts{Timeout: 3 * time.Second})
}

type writer struct {
	http.ResponseWriter
	status int
}

func (w *writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *writer) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *writer) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(p)
}
func (w *writer) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func endpoint(path string) string {
	for _, prefix := range []string{"/jobs/", "/rag/documents/", "/auth/", "/admin/", "/v1/observability/"} {
		if strings.HasPrefix(path, prefix) {
			return prefix + "{id}"
		}
	}
	switch path {
	case "/healthz", "/readyz", "/metrics", "/jobs", "/rag/documents", "/rag/query", "/rag/answer", "/audit", "/webhooks/github", "/v1/chat/completions", "/v1/embeddings":
		return path
	}
	return "other"
}
func (r *Runtime) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		route := endpoint(request.URL.Path)
		method := request.Method
		if method != "GET" && method != "POST" && method != "DELETE" && method != "PUT" && method != "PATCH" {
			method = "other"
		}
		start := time.Now()
		r.Active.Inc()
		defer r.Active.Dec()
		ctx := otel.GetTextMapPropagator().Extract(request.Context(), propagation.HeaderCarrier(request.Header))
		ctx, span := r.provider.Tracer("backend-http").Start(ctx, method+" "+route)
		defer span.End()
		span.SetAttributes(attribute.String("http.request.method", method), attribute.String("http.route", route))
		wrapped := &writer{ResponseWriter: w}
		defer func() {
			status := wrapped.status
			if status == 0 {
				status = 200
			}
			r.Requests.WithLabelValues(r.Service, route, method, strconv.Itoa(status)).Inc()
			r.Duration.WithLabelValues(r.Service, route).Observe(time.Since(start).Seconds())
			span.SetAttributes(attribute.Int("http.response.status_code", status))
			if status >= 500 {
				span.SetStatus(codes.Error, "server_error")
			}
		}()
		next.ServeHTTP(wrapped, request.WithContext(ctx))
	})
}
