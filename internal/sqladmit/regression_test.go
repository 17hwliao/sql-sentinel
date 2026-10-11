package sqladmit

import "testing"

func TestLiteralKeywordsDoNotBecomeSQL(t *testing.T) {
	for _, q := range []string{"SELECT ' INSERT INTO file ' FROM orders", "SELECT ' FOR UPDATE ' FROM orders", "SELECT `status` FROM orders WHERE status=' DROP table '"} {
		if result := Admit(q); !result.Accepted {
			t.Fatalf("safe literal rejected: %s: %+v", q, result)
		}
	}
}

func TestNestedCTEDoesNotHideOuterPhysicalTable(t *testing.T) {
	tables, err := Inspect("SELECT * FROM customers WHERE id IN (WITH customers AS (SELECT customer_id FROM invoices) SELECT customer_id FROM customers)")
	if err != nil || len(tables) != 2 || tables[0] != "customers" || tables[1] != "invoices" {
		t.Fatalf("tables=%v error=%v", tables, err)
	}
}
