package sqladmit

import (
	_ "embed"
	"encoding/json"
	"strings"
	"testing"
)

//go:embed testdata/local-cases.json
var localCorpus []byte

func TestLocalSQLCorpus(t *testing.T) {
	var cases []struct {
		Name, SQL, Tables string
		Accepted          bool
	}
	if err := json.Unmarshal(localCorpus, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			a, err := Analyze(c.SQL)
			if (err == nil) != c.Accepted {
				t.Fatalf("admission=%v error=%v", c.Accepted, err)
			}
			if c.Accepted && strings.Join(a.Tables, ",") != c.Tables {
				t.Fatal("physical scope changed", a.Tables)
			}
		})
	}
}

func BenchmarkSQLAdmissionCorpus(b *testing.B) {
	var cases []struct{ SQL string }
	json.Unmarshal(localCorpus, &cases)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Analyze(cases[i%len(cases)].SQL)
	}
}
