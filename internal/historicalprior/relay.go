package historicalprior

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"time"
)

// Relay is idempotent: ES document IDs are immutable job IDs. It uses a named
// SQL lock so multiple optional relay processes cannot amplify each other.
func Relay(ctx context.Context, db *sql.DB, client *Client) error {
	conn, e := db.Conn(ctx)
	if e != nil {
		return e
	}
	defer conn.Close()
	var locked int
	if e = conn.QueryRowContext(ctx, "SELECT GET_LOCK('sentinel-prior-relay',0)").Scan(&locked); e != nil || locked != 1 {
		return nil
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		var n int
		if e := conn.QueryRowContext(cleanup, "SELECT RELEASE_LOCK('sentinel-prior-relay')").Scan(&n); e != nil || n != 1 {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	rows, e := conn.QueryContext(ctx, "SELECT p.job_id,p.document FROM sql_sentinel_prior_outbox p JOIN sql_sentinel_jobs j ON j.job_id=p.job_id WHERE p.published_at IS NULL AND p.available_at<=UTC_TIMESTAMP(6) AND p.attempts<20 AND j.status='COMPLETED' ORDER BY p.available_at LIMIT 20")
	if e != nil {
		return e
	}
	type item struct {
		id  string
		raw []byte
	}
	var items []item
	for rows.Next() {
		var i item
		if e = rows.Scan(&i.id, &i.raw); e != nil {
			rows.Close()
			return e
		}
		items = append(items, i)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, i := range items {
		var d Document
		e = json.Unmarshal(i.raw, &d)
		if e == nil {
			e = client.Put(ctx, d)
		}
		if e == nil {
			_, e = conn.ExecContext(ctx, "UPDATE sql_sentinel_prior_outbox SET published_at=UTC_TIMESTAMP(6) WHERE job_id=? AND published_at IS NULL", i.id)
		} else {
			_, e = conn.ExecContext(ctx, "UPDATE sql_sentinel_prior_outbox SET attempts=attempts+1,available_at=UTC_TIMESTAMP(6)+INTERVAL 1 MINUTE WHERE job_id=? AND published_at IS NULL", i.id)
		}
		if e != nil {
			return e
		}
	}
	if _, e = conn.ExecContext(ctx, "DELETE FROM sql_sentinel_prior_outbox WHERE expires_at<=UTC_TIMESTAMP(6) LIMIT 1000"); e != nil {
		return e
	}
	_ = client.DeleteExpired(ctx)
	return nil
}
