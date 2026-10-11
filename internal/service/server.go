package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"sqlsentinel/internal/agentgraph"
	"sqlsentinel/internal/historicalprior"
	"sqlsentinel/internal/identity"
	workflow "sqlsentinel/internal/kafka"
	"sqlsentinel/internal/rag"
	"sqlsentinel/internal/sqladmit"
)

var deliveryPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Server struct {
	History       *historicalprior.Client
	Store         *workflow.MySQLStore
	Token         string
	GitHub        *GitHub
	Auth          *identity.Auth
	RAG           *rag.Service
	AnswerModel   agentgraph.DraftNode
	AllowedTables map[string]bool
	slots         chan struct{}
}

func (s *Server) Handler() http.Handler {
	s.slots = make(chan struct{}, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := s.Store.DB.PingContext(ctx); err != nil {
			respond(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		respond(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("POST /webhooks/github", func(w http.ResponseWriter, r *http.Request) {
		if s.GitHub == nil {
			respond(w, 503, map[string]string{"code": "github_not_configured"})
			return
		}
		s.GitHub.Receive(w, r, s.Store)
	})
	mux.Handle("/jobs", s.authorize(http.HandlerFunc(s.receive)))
	mux.Handle("/jobs/", s.authorize(http.HandlerFunc(s.get)))
	mux.Handle("GET /history/search", s.authorize(http.HandlerFunc(s.history)))
	for _, path := range []string{"/auth/tokens", "/auth/revoke", "/audit", "/rag/documents", "/rag/documents/", "/rag/query", "/rag/answer"} {
		mux.Handle(path, s.authorize(http.HandlerFunc(s.extension)))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		case <-r.Context().Done():
			return
		default:
			respond(w, 429, map[string]string{"code": "busy"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) authorize(next http.Handler) http.Handler {
	expected := sha256.Sum256([]byte(s.Token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Auth != nil {
			p, err := s.Auth.Authenticate(r.Context(), r.Header.Get("Authorization"))
			if err != nil {
				status := 401
				if err.Error() == "identity_unavailable" {
					status = 503
				}
				respond(w, status, map[string]string{"code": err.Error()})
				return
			}
			if !s.Auth.Allowed(p, action(r)) {
				respond(w, 403, map[string]string{"code": "forbidden"})
				return
			}
			next.ServeHTTP(w, r.WithContext(identity.With(r.Context(), p)))
			return
		}
		raw := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(raw, "Bearer ")
		actual := sha256.Sum256([]byte(token))
		if !ok || s.Token == "" || subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
			respond(w, 401, map[string]string{"code": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r.WithContext(identity.With(r.Context(), identity.Principal{Subject: "operator", Role: "admin", Tenant: "operator", Project: "default", Tables: []string{"*"}})))
	})
}

func (s *Server) receive(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		respond(w, 405, map[string]string{"code": "method_not_allowed"})
		return
	}
	var in struct {
		DeliveryID string          `json:"delivery_id"`
		PRNumber   int             `json:"pr_number"`
		SQL        string          `json:"sql"`
		Candidate  json.RawMessage `json:"candidate_spec"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil {
		respond(w, 413, map[string]string{"code": "body_too_large"})
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF || !deliveryPattern.MatchString(in.DeliveryID) || in.PRNumber < 1 || len(in.SQL) > 64<<10 || !sqladmit.Admit(in.SQL).Accepted {
		respond(w, 400, map[string]string{"code": "input_rejected"})
		return
	}
	if len(in.Candidate) == 0 {
		in.Candidate = []byte("{}")
	} else {
		if _, err = validateProposal(in.Candidate, in.SQL); err != nil {
			respond(w, 400, map[string]string{"code": "candidate_rejected"})
			return
		}
	}
	p := identity.From(r.Context())
	if !s.tablesAllowed(p, in.SQL) {
		respond(w, 403, map[string]string{"code": "table_not_allowed"})
		return
	}
	result, err := s.Store.Receive(r.Context(), workflow.ReceiveInput{DeliveryID: in.DeliveryID, PRNumber: in.PRNumber, SQL: []byte(in.SQL), CandidateSpec: in.Candidate, Options: workflow.JobOptions{TenantID: p.Tenant, ProjectID: p.Project, Actor: p.Subject}})
	if errors.Is(err, workflow.ErrDeliveryConflict) {
		respond(w, 409, map[string]string{"code": "delivery_conflict"})
		return
	}
	if errors.Is(err, workflow.ErrBacklogFull) {
		respond(w, 429, map[string]string{"code": "job_backlog_full"})
		return
	}
	if err != nil {
		respond(w, 503, map[string]string{"code": "store_unavailable"})
		return
	}
	respond(w, 202, map[string]any{"delivery_id": result.Job.DeliveryID, "job_id": result.Job.ID, "duplicate": result.Duplicate, "status": result.Job.Status})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/approval") || strings.HasSuffix(r.URL.Path, "/benchmark") {
		s.jobAction(w, r)
		return
	}
	if r.Method != "GET" {
		respond(w, 405, map[string]string{"code": "method_not_allowed"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/jobs/")
	id, report := strings.CutSuffix(path, "/report")
	if !deliveryPattern.MatchString(id) {
		respond(w, 400, map[string]string{"code": "invalid_id"})
		return
	}
	if _, err := s.scopedJob(r, id); err != nil {
		jobError(w, err)
		return
	}
	if !report {
		workflow.StatusHandler(s.Store).ServeHTTP(w, r)
		return
	}
	var raw []byte
	err := s.Store.DB.QueryRowContext(r.Context(), `SELECT r.report FROM sql_sentinel_reports r JOIN sql_sentinel_jobs j ON j.job_id=r.job_id WHERE j.delivery_id=? AND j.status IN ('COMPLETED','REJECTED','DEAD_LETTER')`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		respond(w, 404, map[string]string{"code": "report_not_ready"})
		return
	}
	if err != nil {
		respond(w, 503, map[string]string{"code": "store_unavailable"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Sentinel-Report-SHA256", reportDigest(raw))
	w.WriteHeader(200)
	_, _ = w.Write(raw)
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
