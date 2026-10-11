// Package experimentlock fences isolated measurements and orphan cleanup.
package experimentlock

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"time"
)

// Acquire holds a named lock on a dedicated connection until release. Failed
// release discards the connection rather than returning a locked pooled session.
func Acquire(ctx context.Context, conn *sql.Conn, name string) (func(), error) {
	var held sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?,0)", name).Scan(&held); err != nil || !held.Valid || held.Int64 != 1 {
		return nil, errors.New("experiment_busy")
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var released sql.NullInt64
		if err := conn.QueryRowContext(cleanup, "SELECT RELEASE_LOCK(?)", name).Scan(&released); err != nil || !released.Valid || released.Int64 != 1 {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}, nil
}
