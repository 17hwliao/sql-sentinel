package sqladmit

import (
	"slices"
	"testing"
)

func TestCTEDefinitionsKeepPhysicalTableReferences(t *testing.T) {
	for _, c := range []struct {
		name, query string
		tables      []string
	}{
		{"self-name-is-base-table", "WITH secrets AS (SELECT id FROM secrets) SELECT o.id FROM orders o JOIN secrets s ON s.id=o.id", []string{"orders", "secrets"}},
		{"later-name-is-base-table", "WITH first_cte AS (SELECT id FROM secrets), secrets AS (SELECT id FROM orders) SELECT id FROM first_cte", []string{"orders", "secrets"}},
		{"previous-name-is-visible", "WITH secrets AS (SELECT id FROM orders), later AS (SELECT id FROM secrets) SELECT id FROM later", []string{"orders"}},
		{"union-body", "WITH secrets AS (SELECT id FROM secrets UNION ALL SELECT id FROM orders) SELECT id FROM secrets", []string{"orders", "secrets"}},
		{"nested-self-name", "SELECT id FROM orders WHERE id IN (WITH secrets AS (SELECT id FROM secrets) SELECT id FROM secrets)", []string{"orders", "secrets"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			actual, err := Inspect(c.query)
			if err != nil || !slices.Equal(actual, c.tables) {
				t.Fatalf("physical tables=%v expected=%v error=%v", actual, c.tables, err)
			}
		})
	}
}
