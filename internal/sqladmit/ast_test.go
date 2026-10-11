package sqladmit

import "testing"

func TestASTRejectsEffectsAndQuotedCrossSchema(t *testing.T) {
	for _, q := range []string{"SELECT GET_LOCK('x',1)", "SELECT RELEASE_LOCK('x')", "SELECT SLEEP(30)", "SELECT LOAD_FILE('/etc/passwd')", "SELECT custom_function(id) FROM orders", "SELECT * FROM `mysql`.`user`", "SELECT @x:=1", "SELECT 1 INTO @x", "SELECT * FROM orders FOR SHARE", "WITH RECURSIVE t AS (SELECT 1 UNION ALL SELECT 1 FROM t) SELECT * FROM t", "SELECT * FROM orders WHERE id=(DELETE FROM orders)"} {
		if r := Admit(q); r.Accepted {
			t.Fatalf("side effect admitted: %s", q)
		}
	}
}
func TestGenericTablesCTEJoinUnion(t *testing.T) {
	tables, err := Inspect("WITH recent AS (SELECT id FROM customers WHERE id>1) SELECT recent.id FROM recent JOIN invoices i ON i.customer_id=recent.id UNION ALL SELECT customer_id FROM invoices")
	if err != nil || len(tables) != 2 || tables[0] != "customers" || tables[1] != "invoices" {
		t.Fatalf("%v %v", tables, err)
	}
}
