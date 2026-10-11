package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sqlsentinel/internal/agentgraph"
	"sqlsentinel/internal/benchmark"
	"sqlsentinel/internal/historicalprior"
	"time"
)

// OpenRunner constructs only the isolated executor. Worker-side remote mode
// does not open shadow DSNs or instantiate any model adapter.
func OpenRunner(ctx context.Context, lookup func(string) string, control *sql.DB) (Runner, func(), error) {
	r := Runner{Control: control, ModelVersion: lookup("EINO_MODEL")}
	if endpoint := lookup("SQL_SENTINEL_ES_URL"); endpoint != "" {
		var e error
		r.History, e = historicalprior.New(endpoint, lookup("SQL_SENTINEL_ES_API_KEY"), lookup("SQL_SENTINEL_ES_CA"))
		if e != nil {
			return r, func() {}, errors.New("historical_prior_configuration_invalid")
		}
	}
	cleanup := func() {}
	open := func(dsn string) (*sql.DB, error) {
		if dsn == "" {
			return nil, errors.New("shadow_configuration_missing")
		}
		db, e := sql.Open("mysql", dsn)
		if e != nil {
			return nil, e
		}
		db.SetMaxOpenConns(6)
		db.SetMaxIdleConns(2)
		db.SetConnMaxLifetime(5 * time.Minute)
		step, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if e = db.PingContext(step); e != nil {
			db.Close()
			return nil, errors.New("shadow_unavailable")
		}
		return db, nil
	}
	var e error
	r.Baseline, e = open(lookup("SQL_SENTINEL_BASELINE_DSN"))
	if e != nil {
		return r, cleanup, e
	}
	cleanup = func() { r.Baseline.Close() }
	r.Candidate, e = open(lookup("SQL_SENTINEL_CANDIDATE_DSN"))
	if e != nil {
		cleanup()
		return r, func() {}, e
	}
	cleanup = func() { r.Baseline.Close(); r.Candidate.Close() }
	if lookup("EINO_MODEL") != "" {
		r.Model, e = agentgraph.NewOpenAINode(ctx, lookup("OPENAI_API_KEY"), lookup("EINO_MODEL"), lookup("EINO_BASE_URL"))
		if e == nil {
			r.Model, e = agentgraph.NewEinoGraph(r.Model)
		}
		if e != nil {
			cleanup()
			return r, func() {}, errors.New("model_configuration_failed")
		}
	}
	r.AllowedTables, e = AllowedTables(lookup("SQL_SENTINEL_SHADOW_TABLES"))
	if e != nil {
		cleanup()
		return r, func() {}, e
	}
	if raw := lookup("SQL_SENTINEL_REPRESENTATIVE_MANIFEST"); raw != "" {
		r.Representative = &benchmark.Representative{}
		if json.Unmarshal([]byte(raw), r.Representative) != nil {
			cleanup()
			return r, func() {}, errors.New("representative_manifest_invalid")
		}
	}
	return r, cleanup, nil
}
