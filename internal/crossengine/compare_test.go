package crossengine

import (
	"testing"
)

func TestMappingIsExplicitAndBounded(t *testing.T) {
	for _, raw := range []string{"bigint unsigned", "int", "varchar(16)", "datetime(6)", "decimal(10,2)", "json"} {
		if _, e := pgType(raw); e != nil {
			t.Fatal(raw, e)
		}
	}
	for _, raw := range []string{"geometry", "enum('paid','unpaid')", "varbinary(200)", "double unsigned", "varchar(16);DROP TABLE x"} {
		if _, e := pgType(raw); e == nil {
			t.Fatal("unsupported type accepted", raw)
		}
	}
	for _, id := range []string{"pg_catalog.x", "x\"", "X", "../../x"} {
		if _, e := quote(id); e == nil {
			t.Fatal("unsafe identifier", id)
		}
	}
}
