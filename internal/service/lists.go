package service

import (
	"net/http"
	"sqlsentinel/internal/identity"
	"strconv"
	"time"
)

func listPage(r *http.Request) (string, int, bool) {
	after := r.URL.Query().Get("after")
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			return "", 0, false
		}
		limit = n
	}
	return after, limit, after == "" || deliveryPattern.MatchString(after)
}

// listJobs pages through persisted project jobs. SQL is used only to enforce
// the caller's table grant and is never included in list responses.
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	after, limit, ok := listPage(r)
	if !ok {
		respond(w, 400, map[string]string{"code": "invalid_cursor"})
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "RECEIVED", "QUEUED", "RUNNING", "RETRYING", "COMPLETED", "REJECTED", "DEAD_LETTER":
	default:
		respond(w, 400, map[string]string{"code": "invalid_status"})
		return
	}
	p := identity.From(r.Context())
	rows, err := s.Store.DB.QueryContext(r.Context(), `SELECT j.delivery_id,j.status,j.attempts,j.sql_text,j.updated_at FROM sql_sentinel_jobs j JOIN sql_sentinel_job_options o ON o.job_id=j.job_id WHERE JSON_UNQUOTE(JSON_EXTRACT(o.options,'$.tenant_id'))=? AND JSON_UNQUOTE(JSON_EXTRACT(o.options,'$.project_id'))=? AND j.delivery_id>? AND (?='' OR j.status=?) ORDER BY j.delivery_id LIMIT ?`, p.Tenant, p.Project, after, status, status, limit)
	if err != nil {
		respond(w, 503, map[string]string{"code": "store_unavailable"})
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	scanned := 0
	last := ""
	for rows.Next() {
		var id, state string
		var attempts int
		var sql []byte
		var updated time.Time
		if err = rows.Scan(&id, &state, &attempts, &sql, &updated); err != nil {
			break
		}
		scanned++
		last = id
		if s.tablesAllowed(p, string(sql)) {
			out = append(out, map[string]any{"delivery_id": id, "status": state, "attempts": attempts, "updated_at": updated})
		}
	}
	if err != nil || rows.Err() != nil {
		respond(w, 503, map[string]string{"code": "store_unavailable"})
		return
	}
	next := ""
	if scanned == limit {
		next = last
	}
	respond(w, 200, map[string]any{"data": out, "next_after": next})
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	after := r.URL.Query().Get("after")
	if after != "" && !deliveryPattern.MatchString(after) {
		respond(w, 400, map[string]string{"code": "invalid_cursor"})
		return
	}
	p := identity.From(r.Context())
	rows, err := s.Store.DB.QueryContext(r.Context(), `SELECT token_id,subject_id,role_name,expires_at,revoked_at IS NOT NULL FROM sql_sentinel_sessions WHERE tenant_id=? AND project_id=? AND token_id>? ORDER BY token_id LIMIT 50`, p.Tenant, p.Project, after)
	if err != nil {
		respond(w, 503, map[string]string{"code": "store_unavailable"})
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	last := ""
	for rows.Next() {
		var id, subject, role string
		var expires time.Time
		var revoked bool
		if err = rows.Scan(&id, &subject, &role, &expires, &revoked); err != nil {
			break
		}
		last = id
		out = append(out, map[string]any{"token_id": id, "subject": subject, "role": role, "expires_at": expires, "revoked": revoked})
	}
	if err != nil || rows.Err() != nil {
		respond(w, 503, map[string]string{"code": "store_unavailable"})
		return
	}
	next := ""
	if len(out) == 50 {
		next = last
	}
	respond(w, 200, map[string]any{"data": out, "next_after": next})
}

func (s *Server) listDocuments(w http.ResponseWriter, r *http.Request) {
	after, limit, ok := listPage(r)
	if !ok {
		respond(w, 400, map[string]string{"code": "invalid_cursor"})
		return
	}
	p := identity.From(r.Context())
	rows, err := s.Store.DB.QueryContext(r.Context(), `SELECT document_id,version,document_sha256 FROM rag_documents WHERE tenant_id=? AND project_id=? AND document_id>? ORDER BY document_id LIMIT ?`, p.Tenant, p.Project, after, limit)
	if err != nil {
		respond(w, 503, map[string]string{"code": "store_unavailable"})
		return
	}
	defer rows.Close()
	out := []map[string]string{}
	last := ""
	for rows.Next() {
		var id, version, sha string
		if err = rows.Scan(&id, &version, &sha); err != nil {
			break
		}
		last = id
		out = append(out, map[string]string{"document_id": id, "version": version, "sha256": sha})
	}
	if err != nil || rows.Err() != nil {
		respond(w, 503, map[string]string{"code": "store_unavailable"})
		return
	}
	next := ""
	if len(out) == limit {
		next = last
	}
	respond(w, 200, map[string]any{"data": out, "next_after": next})
}
