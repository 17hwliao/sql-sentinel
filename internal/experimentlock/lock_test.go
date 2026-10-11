package experimentlock

import (
	"context"
	"database/sql"
	_ "github.com/go-sql-driver/mysql"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRealLockExcludesConcurrentRunsAndReleasesAfterCancellation(t *testing.T) {
	path := os.Getenv("SQL_SENTINEL_TEST_LOCK_DSN_FILE")
	if path == "" {
		t.Skip("isolated MySQL fixture required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("fixture credential file unreadable")
	}
	db, err := sql.Open("mysql", strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal("fixture unavailable")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := db.Conn(ctx)
	if err != nil {
		t.Fatal("first connection unavailable")
	}
	defer first.Close()
	second, err := db.Conn(ctx)
	if err != nil {
		t.Fatal("second connection unavailable")
	}
	defer second.Close()
	release, err := Acquire(ctx, first, "sentinel-lock-regression")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if undo, err := Acquire(ctx, second, "sentinel-lock-regression"); err == nil {
		undo()
		t.Fatal("concurrent experiment was admitted")
	}
	cancel()
	release()
	next, cancelNext := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelNext()
	undo, err := Acquire(next, second, "sentinel-lock-regression")
	if err != nil {
		t.Fatal("cancelled owner did not release its lock")
	}
	undo()
}
