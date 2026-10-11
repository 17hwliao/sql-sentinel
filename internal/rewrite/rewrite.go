// Package rewrite admits explicit read-only rewrite proposals. Snapshot
// counterexample checks are bounded evidence, never universal equivalence.
package rewrite

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sqlsentinel/internal/sqladmit"
)

var ErrInvalid = errors.New("rewrite_rejected")

type Spec struct {
	Kind string `json:"kind"`
	SQL  string `json:"sql"`
}

func Is(raw []byte) bool {
	var s struct {
		Kind string `json:"kind"`
	}
	return json.Unmarshal(raw, &s) == nil && s.Kind == "rewrite"
}
func Decode(raw []byte, original string) (Spec, error) {
	var s Spec
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 64<<10 || d.Decode(&s) != nil || d.Decode(&struct{}{}) != io.EOF || s.Kind != "rewrite" || s.SQL == "" || len(s.SQL) > 64<<10 {
		return Spec{}, ErrInvalid
	}
	a, e := sqladmit.Analyze(original)
	if e != nil || len(a.Tables) == 0 {
		return Spec{}, ErrInvalid
	}
	b, e := sqladmit.Analyze(s.SQL)
	if e != nil || !slices.Equal(a.Tables, b.Tables) || a.Ordered != b.Ordered {
		return Spec{}, ErrInvalid
	}
	// LIMIT without a total ordering can produce unrelated arbitrary subsets.
	// Conservatively reject every unordered LIMIT, including nested limits.
	if a.UnorderedLimit || b.UnorderedLimit {
		return Spec{}, ErrInvalid
	}
	return s, nil
}
