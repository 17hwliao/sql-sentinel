package service

import (
	"sqlsentinel/internal/identity"
	"testing"
)

func TestCTECannotHideUnauthorizedPhysicalTable(t *testing.T) {
	server := &Server{AllowedTables: map[string]bool{"orders": true}}
	principal := identity.Principal{Tables: []string{"orders"}}
	query := "WITH secrets AS (SELECT id FROM secrets) SELECT o.id FROM orders o JOIN secrets s ON s.id=o.id"
	if server.tablesAllowed(principal, query) {
		t.Fatal("CTE hid unauthorized physical table from admission")
	}
}
