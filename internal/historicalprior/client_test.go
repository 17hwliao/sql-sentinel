package historicalprior

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSearchRevalidatesScopeTablesAndNeverPromotesEvidence(t *testing.T) {
	good := Document{ID: strings.Repeat("a", 64), Tenant: "t", Project: "p", Table: "orders", Tables: []string{"orders"}, Columns: []string{"user_id"}, SQLSHA: strings.Repeat("b", 64), Text: "add_index orders user_id", Level: "L1", Measured: true, Noise: .3, CandidateMedian: 2, Expires: time.Now().Add(time.Hour)}
	wrongTenant := good
	wrongTenant.Tenant = "other"
	wrongTable := good
	wrongTable.Tables = []string{"orders", "secrets"}
	expired := good
	expired.Expires = time.Now().Add(-time.Minute)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q map[string]any
		if json.NewDecoder(r.Body).Decode(&q) != nil {
			t.Error("invalid query")
		}
		raw, _ := json.Marshal(q)
		if !strings.Contains(string(raw), `"tenant_id":"t"`) || q["size"] != float64(20) {
			t.Error("scope or limit missing")
		}
		hits := []any{}
		for _, d := range []Document{good, wrongTenant, wrongTable, expired} {
			hits = append(hits, map[string]any{"_source": d})
		}
		json.NewEncoder(w).Encode(map[string]any{"hits": map[string]any{"hits": hits}})
	}))
	defer srv.Close()
	c, e := New(srv.URL, strings.Repeat("k", 32), "")
	if e != nil {
		t.Fatal(e)
	}
	result, e := c.Search(context.Background(), "t", "p", "orders", "user_id", map[string]bool{"orders": true})
	if e != nil || !result.HistoricalOnly || len(result.Documents) != 1 || result.Documents[0].Level != "L1" || result.Count != 1 || result.MaxReportedNoise != .3 {
		t.Fatal(result, e)
	}
	if _, e = c.Search(context.Background(), "t", "p", "orders", "", map[string]bool{"secrets": true}); e == nil {
		t.Fatal("table ACL bypass")
	}
}
