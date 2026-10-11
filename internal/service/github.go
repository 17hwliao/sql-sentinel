package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	workflow "sqlsentinel/internal/kafka"
	"sqlsentinel/internal/sqladmit"
)

type GitHub struct {
	Secret, Token, BaseURL string
	Repositories           map[string]bool
	Client                 *http.Client
	PublishComments        bool
}
type Source struct {
	Repository string `json:"repository"`
	HeadSHA    string `json:"head_sha"`
	File       string `json:"file"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
}
type pull struct {
	Number int `json:"number"`
	Head   struct {
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
}

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var shaPattern = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

func (g *GitHub) Receive(w http.ResponseWriter, r *http.Request, store *workflow.MySQLStore) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		respond(w, 413, map[string]string{"code": "body_too_large"})
		return
	}
	signature, err := hex.DecodeString(strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256="))
	mac := hmac.New(sha256.New, []byte(g.Secret))
	mac.Write(body)
	if g.Secret == "" || err != nil || !strings.HasPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=") || !hmac.Equal(mac.Sum(nil), signature) {
		respond(w, 401, map[string]string{"code": "invalid_signature"})
		return
	}
	if r.Header.Get("X-GitHub-Event") == "ping" {
		respond(w, 200, map[string]string{"status": "pong"})
		return
	}
	if r.Header.Get("X-GitHub-Event") != "pull_request" {
		respond(w, 200, map[string]string{"status": "ignored_event"})
		return
	}
	var event struct {
		Action     string `json:"action"`
		Number     int    `json:"number"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		PullRequest pull `json:"pull_request"`
	}
	delivery := r.Header.Get("X-GitHub-Delivery")
	if json.Unmarshal(body, &event) != nil || !deliveryPattern.MatchString(delivery) || event.Number < 1 || !repositoryPattern.MatchString(event.Repository.FullName) || !g.Repositories[event.Repository.FullName] {
		respond(w, 400, map[string]string{"code": "event_rejected"})
		return
	}
	switch event.Action {
	case "opened", "synchronize", "reopened", "ready_for_review":
	default:
		respond(w, 200, map[string]string{"status": "ignored_action"})
		return
	}
	actual, err := g.currentPull(r.Context(), event.Repository.FullName, event.Number)
	if err != nil {
		respond(w, 503, map[string]string{"code": "github_unavailable"})
		return
	}
	if actual.Head.SHA != event.PullRequest.Head.SHA {
		respond(w, 200, map[string]string{"status": "superseded"})
		return
	}
	if !shaPattern.MatchString(actual.Head.SHA) || !repositoryPattern.MatchString(actual.Head.Repo.FullName) {
		respond(w, 400, map[string]string{"code": "head_rejected"})
		return
	}
	var jobs []map[string]any
	ignored := 0
	for page := 1; page <= 10; page++ {
		var files []struct {
			Filename string `json:"filename"`
			Status   string `json:"status"`
			Patch    string `json:"patch"`
		}
		if err = g.call(r.Context(), "GET", fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100&page=%d", event.Repository.FullName, event.Number, page), nil, &files); err != nil {
			respond(w, 503, map[string]string{"code": "github_unavailable"})
			return
		}
		for _, file := range files {
			if file.Status == "removed" || !strings.HasSuffix(strings.ToLower(file.Filename), ".sql") {
				continue
			}
			if len(jobs) >= 20 {
				respond(w, 422, map[string]any{"code": "too_many_sql_statements", "jobs": jobs})
				return
			}
			var content struct{ Encoding, Content string }
			escaped := make([]string, 0)
			for _, part := range strings.Split(file.Filename, "/") {
				escaped = append(escaped, url.PathEscape(part))
			}
			if err = g.call(r.Context(), "GET", fmt.Sprintf("/repos/%s/contents/%s?ref=%s", actual.Head.Repo.FullName, strings.Join(escaped, "/"), actual.Head.SHA), nil, &content); err != nil || content.Encoding != "base64" {
				respond(w, 503, map[string]string{"code": "head_file_unavailable"})
				return
			}
			raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(content.Content, "\n", ""))
			if err != nil || len(raw) > 256<<10 {
				respond(w, 422, map[string]string{"code": "sql_file_too_large"})
				return
			}
			statements, err := Statements(string(raw))
			if err != nil {
				ignored++
				continue
			}
			changed := ChangedLines(file.Patch)
			if file.Status != "added" && file.Patch == "" {
				respond(w, 422, map[string]string{"code": "missing_patch_requires_manual_review"})
				return
			}
			for _, statement := range statements {
				if !sqladmit.Admit(statement.SQL).Accepted {
					ignored++
					continue
				}
				relevant := file.Status == "added"
				for line := statement.StartLine; line <= statement.EndLine && !relevant; line++ {
					relevant = changed[line]
				}
				if !relevant {
					continue
				}
				if len(statement.SQL) > 64<<10 || len(jobs) >= 20 {
					respond(w, 422, map[string]any{"code": "sql_limit_exceeded", "jobs": jobs})
					return
				}
				sum := sha256.Sum256([]byte(delivery + "\x00" + file.Filename + "\x00" + strconv.Itoa(statement.StartLine)))
				id := hex.EncodeToString(sum[:])
				source, _ := json.Marshal(Source{Repository: event.Repository.FullName, HeadSHA: actual.Head.SHA, File: file.Filename, StartLine: statement.StartLine, EndLine: statement.EndLine})
				result, err := store.Receive(r.Context(), workflow.ReceiveInput{DeliveryID: id, PRNumber: event.Number, SQL: []byte(statement.SQL), CandidateSpec: []byte("{}"), SourceJSON: source})
				if err != nil {
					if errors.Is(err, workflow.ErrBacklogFull) {
						respond(w, 429, map[string]string{"code": "job_backlog_full"})
						return
					}
					respond(w, 503, map[string]string{"code": "store_unavailable"})
					return
				}
				jobs = append(jobs, map[string]any{"delivery_id": id, "job_id": result.Job.ID, "file": file.Filename, "start_line": statement.StartLine, "end_line": statement.EndLine, "duplicate": result.Duplicate})
			}
		}
		if len(files) < 100 {
			respond(w, 202, map[string]any{"jobs": jobs, "ignored_statements": ignored, "scope": "read-only SQL files, orders-v1 shadow"})
			return
		}
	}
	respond(w, 422, map[string]any{"code": "too_many_changed_files", "jobs": jobs})
}

func (g *GitHub) currentPull(ctx context.Context, repo string, number int) (pull, error) {
	var p pull
	err := g.call(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", repo, number), nil, &p)
	return p, err
}
func (g *GitHub) call(ctx context.Context, method, path string, value, out any) error {
	base := g.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	var body io.Reader
	if value != nil {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(req)
	if err != nil {
		return errors.New("github_request_failed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return errors.New("github_response_limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("github_request_failed")
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
