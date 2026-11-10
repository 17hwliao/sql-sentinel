package service

import (
	"errors"
	"net/http"
	"sqlsentinel/internal/identity"
	workflow "sqlsentinel/internal/kafka"
	"strings"
)

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		respond(w, 405, map[string]string{"code": "method_not_allowed"})
		return
	}
	id := strings.TrimSuffix(jobPathReference(r), "/cancel")
	if !deliveryPattern.MatchString(id) {
		respond(w, 400, map[string]string{"code": "invalid_id"})
		return
	}
	job, err := s.scopedJob(r, id)
	if err != nil {
		jobError(w, err)
		return
	}
	p := identity.From(r.Context())
	if p.Role != "admin" && p.Subject != job.Options.Actor {
		respond(w, 403, map[string]string{"code": "cancel_forbidden"})
		return
	}
	changed, err := s.Store.Cancel(r.Context(), job, p.Subject)
	if errors.Is(err, workflow.ErrRejected) {
		respond(w, 409, map[string]string{"code": "job_already_terminal"})
		return
	}
	if err != nil {
		respond(w, 503, map[string]string{"code": "store_unavailable"})
		return
	}
	respond(w, 200, map[string]any{"job_id": job.ID, "delivery_id": job.DeliveryID, "status": workflow.Cancelled, "changed": changed})
}

func isJobIDPath(r *http.Request) bool {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/jobs/"), "/")
	if len(parts) < 2 || parts[0] != "by-id" || len(parts[1]) != 32 {
		return false
	}
	for _, c := range parts[1] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func jobPathReference(r *http.Request) string {
	path := strings.TrimPrefix(r.URL.Path, "/jobs/")
	if isJobIDPath(r) {
		path = strings.TrimPrefix(path, "by-id/")
	}
	return path
}
