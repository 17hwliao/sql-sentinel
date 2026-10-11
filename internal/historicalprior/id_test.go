package historicalprior

import (
	"strings"
	"testing"
	"time"
)

func TestActualJobIDCreatesStableValidHistoricalDocument(t *testing.T) {
	job := strings.Repeat("a", 32)
	d := Document{ID: DocumentID(job), Tenant: "acceptance", Project: "roadmap-test", Table: "orders", Tables: []string{"orders"}, Columns: []string{"user_id"}, SQLSHA: strings.Repeat("b", 64), Text: "add_index orders user_id", Level: "L1", Expires: time.Now().Add(14 * 24 * time.Hour)}
	if !d.Valid(time.Now()) || DocumentID(job) != d.ID || DocumentID(strings.Repeat("c", 32)) == d.ID {
		t.Fatal("task ID was not correctly bound")
	}
}
