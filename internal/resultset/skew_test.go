package resultset

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"
)

// A deliberately poor non-covering index on a 95%-matching column. This is
// synthetic negative evidence, not a production performance qualification.
func TestPersistentSkewIndexNegativeEvidence(t *testing.T) {
	dsn := os.Getenv("SQL_SENTINEL_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("isolated MySQL required")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var name string
	if db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&name) != nil || name != "sentinel_improvement" {
		t.Fatal("dedicated database required")
	}
	table := fmt.Sprintf("skew_%d", time.Now().UnixNano())
	exec := func(query string) {
		t.Helper()
		if _, e := db.ExecContext(ctx, query); e != nil {
			t.Fatal(e)
		}
	}
	exec("CREATE TABLE " + table + "(id INT PRIMARY KEY, bucket INT NOT NULL, payload VARCHAR(512) NOT NULL, KEY bad_bucket(bucket))")
	defer db.ExecContext(context.Background(), "DROP TABLE "+table)
	for batch := 0; batch < 100; batch++ {
		exec(fmt.Sprintf("INSERT INTO %s WITH RECURSIVE n AS (SELECT 0 AS v UNION ALL SELECT v+1 FROM n WHERE v<999) SELECT n.v+%d+1,IF(MOD(n.v,100)<95,1,2),REPEAT(SHA2(n.v+%d,256),8) FROM n", table, batch*1000, batch*1000))
	}
	exec("ANALYZE TABLE " + table)
	queries := []string{"SELECT SUM(LENGTH(payload)) FROM " + table + " IGNORE INDEX(bad_bucket) WHERE bucket=1", "SELECT SUM(LENGTH(payload)) FROM " + table + " FORCE INDEX(bad_bucket) WHERE bucket=1"}
	var plans []string
	for _, q := range queries {
		var plan string
		if db.QueryRowContext(ctx, "EXPLAIN ANALYZE "+q).Scan(&plan) != nil {
			t.Fatal("actual plan")
		}
		plans = append(plans, plan)
	}
	var times [2][]float64
	for round := 0; round < 25; round++ {
		for k := 0; k < 2; k++ {
			i := (round + k) % 2
			start := time.Now()
			var sum int64
			if db.QueryRowContext(ctx, queries[i]).Scan(&sum) != nil || sum != 95000*512 {
				t.Fatal("negative candidate changed rows")
			}
			if round >= 20 {
				times[i] = append(times[i], float64(time.Since(start).Microseconds())/1000)
			}
		}
	}
	median := func(a []float64) float64 { b := append([]float64{}, a...); sort.Float64s(b); return b[len(b)/2] }
	ratio := median(times[1]) / median(times[0])
	report, _ := json.Marshal(map[string]any{"scope": "synthetic L1 negative, not L3", "rows": 100000, "matching_rows": 95000, "noise_rounds": 20, "formal_rounds": 5, "baseline_ms": times[0], "candidate_ms": times[1], "candidate_over_baseline": ratio, "plans": plans, "result_equal": true})
	t.Log(string(report))
	if ratio <= 1 {
		t.Fatal("negative performance evidence inconclusive; retain timings and rerun on an idle machine")
	}
}
