package telemetry

import (
	"context"
	"database/sql"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"net/http"
	"time"
)

type Transport struct{ Base http.RoundTripper }

func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	ctx, span := otel.Tracer("backend-client").Start(r.Context(), "upstream.request")
	defer span.End()
	clone := r.Clone(ctx)
	clone.Header = r.Header.Clone()
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(clone.Header))
	response, err := base.RoundTrip(clone)
	if err != nil {
		span.SetStatus(codes.Error, "upstream_unavailable")
	}
	return response, err
}
func (r *Runtime) WatchSQL(ctx context.Context, db *sql.DB, name, help, query string) {
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
	r.Registry.MustRegister(gauge)
	go func() {
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			step, cancel := context.WithTimeout(ctx, time.Second)
			var value float64
			if db.QueryRowContext(step, query).Scan(&value) == nil {
				gauge.Set(value)
			} else {
				gauge.Set(-1)
			}
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}
