package resultset

import (
	"context"
	"database/sql"
	_ "github.com/go-sql-driver/mysql"
	"os"
	"testing"
	"time"
)

func TestPersistentSQLCounterexamples(t *testing.T) {
	dsn := os.Getenv("SQL_SENTINEL_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("isolated sentinel_improvement database required")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal("database open failed")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var name string
	if err = db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&name); err != nil || name != "sentinel_improvement" {
		t.Fatal("dedicated test database required")
	}
	for _, statement := range []string{
		"CREATE TABLE orders(id INT PRIMARY KEY,user_id INT,status VARCHAR(16),amount DECIMAL(10,2))",
		"CREATE TABLE customers(id INT PRIMARY KEY,name VARCHAR(16))",
		"CREATE TABLE secrets(id INT PRIMARY KEY)",
		"INSERT INTO orders VALUES(1,1,'paid',10),(2,1,'paid',10),(3,2,NULL,NULL),(4,2,'new',0),(5,3,'paid',20),(6,4,'new',0)",
		"INSERT INTO customers VALUES(1,'Acme'),(2,NULL),(3,'Acme')",
		"INSERT INTO secrets VALUES(1),(6)",
	} {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			t.Fatal("fixture setup failed", err)
		}
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		db.ExecContext(cleanup, "DROP TABLE IF EXISTS orders,customers,secrets")
	}()
	for _, c := range []struct {
		name, a, b     string
		ordered, equal bool
	}{
		{"predicate-equivalent", "SELECT id FROM orders WHERE id>=1", "SELECT id FROM orders WHERE id>0", false, true},
		{"nullable-equivalent", "SELECT id FROM orders WHERE status='paid'", "SELECT id FROM orders WHERE COALESCE(status,'')='paid'", false, true},
		{"null-is-not-empty", "SELECT id FROM orders WHERE status IS NULL", "SELECT id FROM orders WHERE status=''", false, false},
		{"null-negation", "SELECT id FROM orders WHERE status IS NULL", "SELECT id FROM orders WHERE status!='paid'", false, false},
		{"duplicates", "SELECT user_id FROM orders", "SELECT DISTINCT user_id FROM orders", false, false},
		{"row-order", "SELECT id FROM orders ORDER BY id", "SELECT id FROM orders ORDER BY id DESC", true, false},
		{"unordered-multiset", "SELECT id FROM orders ORDER BY id", "SELECT id FROM orders ORDER BY id DESC", false, true},
		{"column-label", "SELECT id AS original FROM orders", "SELECT id AS changed FROM orders", false, false},
		{"aggregate-null", "SELECT COUNT(amount) AS n FROM orders", "SELECT COUNT(*) AS n FROM orders", false, false},
		{"join-cardinality", "SELECT o.id FROM orders o LEFT JOIN customers c ON c.id=o.user_id", "SELECT o.id FROM orders o JOIN customers c ON c.id=o.user_id", false, false},
		{"union-duplicates", "SELECT user_id FROM orders UNION ALL SELECT user_id FROM orders", "SELECT user_id FROM orders UNION SELECT user_id FROM orders", false, false},
		{"limit-selection", "SELECT id FROM orders ORDER BY id LIMIT 2", "SELECT id FROM orders ORDER BY id DESC LIMIT 2", true, false},
		{"cte-self-name-reads-physical-table", "WITH secrets AS (SELECT id FROM secrets) SELECT o.id FROM orders o JOIN secrets s ON s.id=o.id ORDER BY o.id", "SELECT o.id FROM orders o JOIN secrets s ON s.id=o.id ORDER BY o.id", true, true},
		{"cte-forward-name-reads-physical-table", "WITH first_cte AS (SELECT id FROM secrets), secrets AS (SELECT id FROM orders) SELECT id FROM first_cte ORDER BY id", "SELECT id FROM secrets ORDER BY id", true, true},
		{"nested-self-name-uses-visible-outer-cte", "WITH secrets AS (SELECT id FROM orders) SELECT id FROM secrets WHERE id IN (WITH secrets AS (SELECT id FROM secrets) SELECT id FROM secrets) ORDER BY id", "SELECT id FROM orders ORDER BY id", true, true},
		{"derived-table-equivalence", "SELECT id FROM orders WHERE user_id=1 ORDER BY id", "SELECT d.id FROM (SELECT id,user_id FROM orders) d WHERE d.user_id=1 ORDER BY d.id", true, true},
		{"quoted-cte-self-table", "WITH `secrets` AS (SELECT id FROM `secrets`) SELECT id FROM secrets ORDER BY id", "SELECT id FROM secrets ORDER BY id", true, true},
		{"cte-chain", "WITH a AS (SELECT id FROM orders), b AS (SELECT id FROM a WHERE id<4) SELECT id FROM b ORDER BY id", "SELECT id FROM orders WHERE id<4 ORDER BY id", true, true},
		{"cte-shadow-different-data", "WITH secrets AS (SELECT id FROM orders) SELECT id FROM secrets", "SELECT id FROM secrets", false, false},
		{"exists-equivalence", "SELECT o.id FROM orders o WHERE EXISTS (SELECT 1 FROM customers c WHERE c.id=o.user_id)", "SELECT o.id FROM orders o JOIN customers c ON c.id=o.user_id", false, true},
		{"not-in-null-counterexample", "SELECT id FROM orders WHERE status NOT IN ('paid',NULL)", "SELECT id FROM orders WHERE status!='paid'", false, false},
		{"case-null-equivalence", "SELECT CASE WHEN amount IS NULL THEN 0 ELSE amount END AS n FROM orders", "SELECT COALESCE(amount,0) AS n FROM orders", false, true},
		{"empty-aggregate-null", "SELECT SUM(amount) AS n FROM orders WHERE id<0", "SELECT COALESCE(SUM(amount),0) AS n FROM orders WHERE id<0", false, false},
		{"having-vs-where-equivalence", "SELECT user_id,COUNT(*) AS n FROM orders GROUP BY user_id HAVING user_id=1", "SELECT user_id,COUNT(*) AS n FROM orders WHERE user_id=1 GROUP BY user_id", false, true},
		{"left-join-filter-changes-rows", "SELECT o.id FROM orders o LEFT JOIN customers c ON c.id=o.user_id AND c.name='Acme'", "SELECT o.id FROM orders o LEFT JOIN customers c ON c.id=o.user_id WHERE c.name='Acme'", false, false},
		{"limit-offset-selection", "SELECT id FROM orders ORDER BY id LIMIT 2 OFFSET 1", "SELECT id FROM orders ORDER BY id LIMIT 2", true, false},
		{"union-distinct-equivalence", "SELECT user_id FROM orders UNION SELECT user_id FROM orders", "SELECT DISTINCT user_id FROM orders", false, true},
		{"numeric-conversion-counterexample", "SELECT id FROM orders WHERE CAST(user_id AS CHAR)='01'", "SELECT id FROM orders WHERE user_id='01'", false, false},
		{"collation-counterexample", "SELECT id FROM customers WHERE name COLLATE utf8mb4_0900_ai_ci='acme'", "SELECT id FROM customers WHERE name COLLATE utf8mb4_bin='acme'", false, false},
		{"alias-expression-label", "SELECT id+0 AS id FROM orders", "SELECT id FROM orders", false, true},
		{"null-safe-equals", "SELECT id FROM orders WHERE status<=>NULL", "SELECT id FROM orders WHERE status IS NULL", false, true},
		{"null-equality-counterexample", "SELECT id FROM orders WHERE status=NULL", "SELECT id FROM orders WHERE status IS NULL", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, _, e := Read(ctx, db, c.a, c.ordered)
			if e != nil {
				t.Fatal(e)
			}
			b, _, e := Read(ctx, db, c.b, c.ordered)
			if e != nil {
				t.Fatal(e)
			}
			if (a == b) != c.equal {
				t.Fatal("counterexample classification wrong")
			}
		})
	}
}
