package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// RunComments retries durable terminal jobs. Network errors never erase a
// pending comment. The marker deduplicates a crash after GitHub accepted it.
func (g *GitHub) RunComments(ctx context.Context, db *sql.DB) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		step, cancel := context.WithTimeout(ctx, 40*time.Second)
		err := g.PublishPending(step, db)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Print("github_comment_publish_failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (g *GitHub) PublishPending(ctx context.Context, db *sql.DB) error {
	if !g.PublishComments {
		return nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var lock sql.NullInt64
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK('sentinel-comment-publisher',0)").Scan(&lock); err != nil {
		return err
	}
	if !lock.Valid || lock.Int64 != 1 {
		return nil
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		var result sql.NullInt64
		if err := conn.QueryRowContext(clean, "SELECT RELEASE_LOCK('sentinel-comment-publisher')").Scan(&result); err != nil || !result.Valid || result.Int64 != 1 {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	rows, err := db.QueryContext(ctx, `SELECT j.job_id,j.pr_number,j.status,s.source FROM sql_sentinel_sources s JOIN sql_sentinel_jobs j ON j.job_id=s.job_id WHERE s.comment_published_at IS NULL AND j.status IN ('COMPLETED','REJECTED','DEAD_LETTER') ORDER BY j.updated_at LIMIT 5`)
	if err != nil {
		return err
	}
	type item struct {
		id     string
		pr     int
		status string
		source Source
	}
	var items []item
	for rows.Next() {
		var it item
		var raw []byte
		if err = rows.Scan(&it.id, &it.pr, &it.status, &raw); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &it.source); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, it := range items {
		if !g.Repositories[it.source.Repository] {
			return errors.New("repository_not_allowed")
		}
		state := "published"
		current, err := g.currentPull(ctx, it.source.Repository, it.pr)
		if err != nil {
			return err
		}
		if current.Head.SHA != it.source.HeadSHA {
			state = "superseded"
		} else {
			file := strings.NewReplacer("`", "", "\r", "", "\n", "").Replace(it.source.File)
			body := fmt.Sprintf("<!-- sql-sentinel:%s -->\nSQL Sentinel: **%s**\n\nSource: `%s` lines %d–%d, head `%s`.\n\nEvidence boundary: L1 EXPLAIN and isolated orders-v1 shadow verification. This report does not establish a performance improvement. Full artifacts are available from the authenticated job report API. No production DDL was executed.", it.id, it.status, file, it.source.StartLine, it.source.EndLine, it.source.HeadSHA)
			if err = g.upsertComment(ctx, it.source.Repository, it.pr, it.id, body); err != nil {
				return err
			}
		}
		if _, err = db.ExecContext(ctx, `UPDATE sql_sentinel_sources SET comment_published_at=UTC_TIMESTAMP(6),comment_state=? WHERE job_id=? AND comment_published_at IS NULL`, state, it.id); err != nil {
			return err
		}
	}
	return nil
}
func (g *GitHub) upsertComment(ctx context.Context, repo string, pr int, id, body string) error {
	marker := "<!-- sql-sentinel:" + id + " -->"
	for page := 1; page <= 20; page++ {
		var comments []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		}
		if err := g.call(ctx, "GET", fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100&page=%d", repo, pr, page), nil, &comments); err != nil {
			return err
		}
		for _, comment := range comments {
			if strings.Contains(comment.Body, marker) {
				return g.call(ctx, "PATCH", fmt.Sprintf("/repos/%s/issues/comments/%d", repo, comment.ID), map[string]string{"body": body}, nil)
			}
		}
		if len(comments) < 100 {
			return g.call(ctx, "POST", fmt.Sprintf("/repos/%s/issues/%d/comments", repo, pr), map[string]string{"body": body}, nil)
		}
	}
	return errors.New("comment_pagination_limit")
}
