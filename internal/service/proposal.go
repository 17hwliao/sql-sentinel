package service

import (
	"bytes"
	"encoding/json"
	"sqlsentinel/internal/candidate"
	"sqlsentinel/internal/rewrite"
	"sqlsentinel/internal/shadowgate"
)

func validateProposal(raw []byte, original string) ([]byte, error) {
	if rewrite.Is(raw) {
		s, e := rewrite.Decode(raw, original)
		if e != nil {
			return nil, e
		}
		return json.Marshal(s)
	}
	s, e := candidate.DecodeStrict(bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	if _, e = shadowgate.Prepare(original, s); e != nil {
		return nil, e
	}
	return json.Marshal(s)
}
