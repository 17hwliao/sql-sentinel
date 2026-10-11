package service

import (
	"errors"
	"sqlsentinel/internal/shadowdata"
	"strings"
)

func AllowedTables(raw string) (map[string]bool, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "orders"
	}
	out := map[string]bool{}
	for _, table := range strings.Split(raw, ",") {
		table = strings.TrimSpace(table)
		if _, err := shadowdata.Quote(table); err != nil || table != strings.ToLower(table) {
			return nil, errors.New("shadow_allowlist_invalid")
		}
		out[table] = true
	}
	if len(out) > 16 {
		return nil, errors.New("shadow_allowlist_limit")
	}
	return out, nil
}
