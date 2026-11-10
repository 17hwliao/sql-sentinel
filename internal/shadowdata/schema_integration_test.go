package shadowdata

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// This integration check uses the same explicitly configured disposable
// control schema as the Agent acceptance suite, never an ambient production DSN.
func TestDescribeFunctionalIndexesMySQLIntegration(t *testing.T) {
	file := os.Getenv("SQL_SENTINEL_AGENT_CONTROL_DSN_FILE")
	if file == "" {
		t.Skip("dedicated disposable MySQL DSN file not configured")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("private test DSN missing")
	}
	db, err := sql.Open("mysql", strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal("test DSN invalid")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var schema string
	if db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&schema) != nil || schema != "sentinel_improvement" {
		t.Fatal("isolated sentinel_improvement database required")
	}
	name := fmt.Sprintf("review_func_%d", time.Now().UnixNano())
	if _, err = db.ExecContext(ctx, "CREATE TABLE "+name+" (id INT PRIMARY KEY,title VARCHAR(190),INDEX idx_title (title),INDEX idx_title_lower ((LOWER(title))),INDEX idx_mixed (id,(LOWER(title))))"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if _, e := db.ExecContext(cleanup, "DROP TABLE "+name); e != nil {
			t.Error("test table cleanup failed")
		}
	}()
	tables, err := Describe(ctx, db, []string{name})
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || len(tables[0].Columns) != 2 || len(tables[0].Indexes) != 5 {
		t.Fatalf("unexpected schema: %+v", tables)
	}
	for _, column := range tables[0].Columns {
		if column.Name != "id" && column.Name != "title" {
			t.Fatalf("expression became a physical column: %+v", column)
		}
	}
	seen := map[string]bool{}
	for _, index := range tables[0].Indexes {
		switch index.Name {
		case "PRIMARY":
			if index.Column != "id" || index.Expression != "" || !index.Unique {
				t.Fatalf("ordinary primary key changed: %+v", index)
			}
		case "idx_title":
			if index.Column != "title" || index.Expression != "" || index.Sequence != 1 || index.Unique {
				t.Fatalf("ordinary index changed: %+v", index)
			}
		case "idx_title_lower":
			if index.Column != "" || !strings.Contains(strings.ToLower(index.Expression), "lower") || !strings.Contains(index.Expression, "title") {
				t.Fatalf("functional index lost: %+v", index)
			}
		case "idx_mixed":
			if index.Sequence == 1 && (index.Column != "id" || index.Expression != "") {
				t.Fatalf("mixed physical key changed: %+v", index)
			}
			if index.Sequence == 2 && (index.Column != "" || index.Expression == "") {
				t.Fatalf("mixed expression lost: %+v", index)
			}
		}
		seen[fmt.Sprintf("%s:%d", index.Name, index.Sequence)] = true
	}
	for _, key := range []string{"PRIMARY:1", "idx_title:1", "idx_title_lower:1", "idx_mixed:1", "idx_mixed:2"} {
		if !seen[key] {
			t.Fatalf("missing index part %s", key)
		}
	}
}

func TestIndexJSONCompatibility(t *testing.T) {
	raw, err := json.Marshal(Index{Name: "idx_title", Column: "title", Sequence: 1, Unique: false})
	if err != nil || string(raw) != `{"name":"idx_title","column":"title","sequence":1,"unique":false}` {
		t.Fatalf("ordinary index JSON contract changed: %s %v", raw, err)
	}
}
