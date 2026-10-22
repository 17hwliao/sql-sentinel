package resultset

import "testing"

func TestExactDecimalWireNormalization(t *testing.T) {
	for _, c := range []struct{ input, want string }{{"0.00", "0"}, {"-0.000", "0"}, {"1.2300", "123/100"}, {"9007199254740993.00", "9007199254740993"}, {"0.0000000000000000001", "1/10000000000000000000"}} {
		got, err := Canonical([]byte(c.input), "DECIMAL")
		if err != nil || string(got) != c.want {
			t.Fatalf("decimal precision lost for %s", c.input)
		}
	}
	if _, err := Canonical([]byte("not-a-decimal"), "NUMERIC"); err == nil {
		t.Fatal("invalid numeric accepted")
	}
}
