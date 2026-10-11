package service

import (
	"context"
	"net/http"
	"sqlsentinel/internal/identity"
	"time"
)

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		respond(w, 503, map[string]string{"code": "history_not_configured"})
		return
	}
	p := identity.From(r.Context())
	allowed := map[string]bool{}
	for _, t := range p.Tables {
		allowed[t] = true
	}
	table := r.URL.Query().Get("table")
	if !allowed[table] && !allowed["*"] {
		respond(w, 404, map[string]string{"code": "table_not_found"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	result, e := s.History.Search(ctx, p.Tenant, p.Project, table, r.URL.Query().Get("q"), allowed)
	if e != nil {
		respond(w, 503, map[string]string{"code": "history_unavailable"})
		return
	}
	respond(w, 200, result)
}
