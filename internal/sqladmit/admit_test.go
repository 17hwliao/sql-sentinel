package sqladmit

import "testing"

func TestAdmission(t *testing.T) {
	for _, tc := range []struct {
		s    string
		ok   bool
		code string
	}{
		{"SELECT * FROM orders WHERE note='; DROP'", true, ""},
		{"SELECT 1; SELECT 2", false, "multiple_statements"},
		{"SELECT 1; -- trailing comment", true, ""},
		{"\uFEFFSELECT 1; -- UTF-8 BOM and trailing comment", true, ""},
		{"UPDATE orders SET status='x'", false, "not_readonly_select"},
		{"SELECT * INTO OUTFILE 'x' FROM orders", false, "select_into_file"},
		{"SELECT * FROM orders FOR UPDATE", false, "locking_read"},
		{"SELECT * FROM orders LOCK IN /* no-op */ SHARE MODE", false, "locking_read"},
		{"/* DELETE */ SELECT * FROM orders", true, ""},
		{"SELECT 1 /*!50000 INTO OUTFILE 'unsafe' */", false, "executable_comment_not_supported"},
		{"SELECT 'it''s safe'", true, ""},
	} {
		r := Admit(tc.s)
		if r.Accepted != tc.ok || r.ReasonCode != tc.code {
			t.Fatalf("%q => %+v", tc.s, r)
		}
	}
}
func TestSignalsIgnoreCommentsAndStrings(t *testing.T) {
	r := Admit("SELECT note FROM orders -- SELECT * LIKE '%x'\n WHERE id=1")
	if len(r.Signals) != 0 {
		t.Fatal(r.Signals)
	}
	r = Admit("SELECT * FROM orders WHERE name LIKE '%x'")
	if len(r.Signals) != 2 {
		t.Fatal(r.Signals)
	}
	r = Admit("SELECT id FROM orders WHERE LOWER(email) = 'a@example.test'")
	if len(r.Signals) != 1 || r.Signals[0] != "function_on_probable_column" {
		t.Fatal(r.Signals)
	}
	r = Admit("SELECT lower FROM orders")
	if len(r.Signals) != 0 {
		t.Fatal(r.Signals)
	}
}
