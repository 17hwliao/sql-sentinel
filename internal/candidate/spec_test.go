package candidate

import (
	"strings"
	"testing"
)

func valid() Spec {
	return Spec{Table: "orders", IndexName: "idx_cand_user_status_created", Columns: []Column{{Name: "user_id"}, {Name: "status", Direction: "ASC"}, {Name: "created_at", Direction: "DESC"}}}
}

func TestCompileCreateIndexIsDeterministic(t *testing.T) {
	got, err := CompileCreateIndex(valid())
	if err != nil {
		t.Fatal(err)
	}
	want := "CREATE INDEX `idx_cand_user_status_created` ON `orders` (`user_id` ASC, `status` ASC, `created_at` DESC)"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Risk: an Agent turns structured output into a raw DDL injection channel.
func TestValidateRejectsUnsafeSpecs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Spec)
	}{
		{"table injection", func(s *Spec) { s.Table = "orders; DROP TABLE orders" }},
		{"index prefix", func(s *Spec) { s.IndexName = "idx_real" }},
		{"column expression", func(s *Spec) { s.Columns[0].Name = "LOWER(user_id)" }},
		{"duplicate", func(s *Spec) { s.Columns[1].Name = "user_id" }},
		{"direction", func(s *Spec) { s.Columns[0].Direction = "DESC; DROP" }},
		{"too many", func(s *Spec) { s.Columns = append(s.Columns, Column{Name: "a"}, Column{Name: "b"}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := valid()
			tc.mutate(&s)
			if _, err := CompileCreateIndex(s); err == nil {
				t.Fatal("unsafe spec compiled")
			}
		})
	}
}

func TestDecodeStrictRejectsRawSQLAndUnknownFields(t *testing.T) {
	for _, input := range []string{
		`{"table":"orders","index_name":"idx_cand_x","columns":[{"name":"id"}],"sql":"DROP TABLE orders"}`,
		`{"table":"orders","index_name":"idx_cand_x","columns":[{"name":"id"}],"ddl":"CREATE INDEX evil"}`,
		`{"table":"orders","index_name":"idx_cand_x","columns":[{"name":"id"}]} {}`,
	} {
		if _, err := DecodeStrict(strings.NewReader(input)); err == nil {
			t.Fatalf("input should fail: %s", input)
		}
	}
}
