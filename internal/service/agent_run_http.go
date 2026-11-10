package service

import (
	"errors"
	"net/http"
	"regexp"
	"sqlsentinel/internal/agentrun"
	"sqlsentinel/internal/identity"
	"strings"
)

var runIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func runHTTPError(w http.ResponseWriter, err error) {
	code := agentrun.ErrorCode(err)
	status := 503
	switch {
	case errors.Is(err, agentrun.ErrNotFound):
		status = 404
	case errors.Is(err, agentrun.ErrConflict):
		status = 409
	case errors.Is(err, agentrun.ErrForbidden):
		status = 403
	case errors.Is(err, agentrun.ErrInvalid), errors.Is(err, agentrun.ErrSQLRequired):
		status = 400
	case errors.Is(err, agentrun.ErrBudget):
		status = 429
	}
	respond(w, status, map[string]string{"code": code})
}
func (s *Server) agentRunHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		respond(w, 400, map[string]string{"code": "invalid_query"})
		return
	}
	p := identity.From(r.Context())
	if s.AgentRuns == nil {
		if r.URL.Path == "/agent/config" && r.Method == "GET" {
			respond(w, 200, agentrun.PublicConfig{Enabled: false, ModelAliases: []string{}, TenantID: p.Tenant, ProjectID: p.Project, Tables: p.Tables, MaxModelCalls: 4, MaxJobs: 1, RunSeconds: 180, InnerModelCalls: 5, InnerEvaluations: 2})
			return
		}
		respond(w, 503, map[string]string{"code": "agent_not_configured"})
		return
	}
	if r.URL.Path == "/agent/config" {
		if r.Method != "GET" {
			respond(w, 405, map[string]string{"code": "method_not_allowed"})
			return
		}
		respond(w, 200, s.AgentRuns.Configuration(p))
		return
	}
	if r.URL.Path == "/agent/runs" {
		switch r.Method {
		case "GET":
			runs, err := s.AgentRuns.List(r.Context(), p)
			if err != nil {
				runHTTPError(w, err)
				return
			}
			respond(w, 200, map[string]any{"runs": runs})
		case "POST":
			var in agentrun.Input
			if decode(w, r, &in) != nil {
				respond(w, 400, map[string]string{"code": "agent_run_input_invalid"})
				return
			}
			out, duplicate, err := s.AgentRuns.Create(r.Context(), p, in)
			if err != nil {
				runHTTPError(w, err)
				return
			}
			if duplicate {
				w.Header().Set("X-Sentinel-Duplicate", "true")
			}
			respond(w, 202, out)
		default:
			respond(w, 405, map[string]string{"code": "method_not_allowed"})
		}
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/agent/runs/")
	if request, ok := strings.CutPrefix(path, "by-request/"); ok {
		if r.Method != "GET" {
			respond(w, 405, map[string]string{"code": "method_not_allowed"})
			return
		}
		if !deliveryPattern.MatchString(request) {
			respond(w, 404, map[string]string{"code": "agent_run_not_found"})
			return
		}
		out, err := s.AgentRuns.GetByRequest(r.Context(), p, request)
		if err != nil {
			runHTTPError(w, err)
			return
		}
		respond(w, 200, out)
		return
	}
	id, cancel := strings.CutSuffix(path, "/cancel")
	if !runIDPattern.MatchString(id) {
		respond(w, 404, map[string]string{"code": "agent_run_not_found"})
		return
	}
	if (cancel && r.Method != "POST") || (!cancel && r.Method != "GET") {
		respond(w, 405, map[string]string{"code": "method_not_allowed"})
		return
	}
	var out agentrun.Run
	var err error
	if cancel {
		out, err = s.AgentRuns.Cancel(r.Context(), p, id)
	} else {
		out, err = s.AgentRuns.Get(r.Context(), p, id)
	}
	if err != nil {
		runHTTPError(w, err)
		return
	}
	respond(w, 200, out)
}
