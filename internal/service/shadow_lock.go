package service

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Do not issue a server-side blocking GET_LOCK with a cancellable client
// context. MySQL can keep waiting after the driver returns, later acquiring a
// lock on behalf of an already cancelled executor. Each probe is nonblocking
// and is allowed to finish before the caller observes cancellation/cleans up.
func acquireShadowLock(ctx context.Context, conn *sql.Conn) error {
	wait, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	for {
		if err := wait.Err(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("shadow_busy")
		}
		probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		var locked sql.NullInt64
		err := conn.QueryRowContext(probe, "SELECT GET_LOCK(CONCAT('sentinel-shadow:',DATABASE()),0)").Scan(&locked)
		cancel()
		if err != nil || !locked.Valid {
			return errors.New("shadow_lock_failed")
		}
		if locked.Int64 == 1 {
			return nil
		} // caller must install cleanup before checking ctx
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-wait.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
