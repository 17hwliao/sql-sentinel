package rewrite

import (
	"encoding/json"
	"testing"
)

func TestAdmissionCannotWidenScopeOrAcceptDML(t *testing.T) {
	q := "SELECT id FROM orders ORDER BY id"
	for _, raw := range []string{`{"kind":"rewrite","sql":"DELETE FROM orders"}`, `{"kind":"rewrite","sql":"SELECT id FROM customers ORDER BY id"}`, `{"kind":"rewrite","sql":"SELECT id FROM orders"}`, `{"kind":"rewrite","sql":"SELECT id FROM orders ORDER BY id","ddl":"drop table orders"}`} {
		if _, e := Decode([]byte(raw), q); e == nil {
			t.Fatal("accepted", raw)
		}
	}
	if _, e := Decode([]byte(`{"kind":"rewrite","sql":"SELECT id FROM orders WHERE 1=1 ORDER BY id"}`), q); e != nil {
		t.Fatal(e)
	}
}

func TestRewriteLimitUsesEveryASTScope(t *testing.T) {
	for _, c := range []struct {
		sql      string
		accepted bool
	}{
		{"SELECT 'limit' FROM orders", true},
		{"SELECT id FROM orders WHERE status='unlimited'", true},
		{"SELECT id FROM orders LIMIT 1", false},
		{"SELECT id FROM (SELECT id FROM orders LIMIT 1) x ORDER BY id", false},
		{"WITH x AS (SELECT id FROM orders LIMIT 1) SELECT id FROM x ORDER BY id", false},
		{"SELECT id FROM orders WHERE id IN (SELECT id FROM orders LIMIT 1) ORDER BY id", false},
		{"SELECT id FROM orders ORDER BY id LIMIT 1", true},
		{"SELECT id FROM (SELECT id FROM orders ORDER BY id LIMIT 1) x ORDER BY id", true},
	} {
		raw, _ := json.Marshal(Spec{Kind: "rewrite", SQL: c.sql})
		_, err := Decode(raw, c.sql)
		if (err == nil) != c.accepted {
			t.Fatalf("query=%s accepted=%v err=%v", c.sql, c.accepted, err)
		}
	}
}
