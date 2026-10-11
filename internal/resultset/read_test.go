package resultset

import (
	"bytes"
	"testing"
)

func TestWireCanonicalization(t *testing.T) {
	a, e := Canonical([]byte("2024-01-01 12:00:00"), "DATETIME")
	b, f := Canonical([]byte("2024-01-01T12:00:00Z"), "TIMESTAMP")
	if e != nil || f != nil || !bytes.Equal(a, b) {
		t.Fatal(string(a), string(b), e, f)
	}
	a, e = Canonical([]byte(`{"b":2, "a":1}`), "JSON")
	b, f = Canonical([]byte(`{"a":1,"b":2}`), "JSONB")
	if e != nil || f != nil || !bytes.Equal(a, b) {
		t.Fatal("JSON wire difference not canonicalized")
	}
	if _, e = Canonical([]byte("0000-00-00"), "DATE"); e == nil {
		t.Fatal("invalid date normalized silently")
	}
}
