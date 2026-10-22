// Package resultset provides bounded, NULL-safe result fingerprints. Ordered
// queries preserve row order; JSON and date/time wire formats are canonicalized.
package resultset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"sort"
	"strings"
	"time"
)

type DB interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func Digest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func Canonical(raw []byte, kind string) ([]byte, error) {
	if raw == nil {
		return nil, nil
	}
	kind = strings.ToUpper(kind)
	if kind == "DECIMAL" || kind == "NEWDECIMAL" || kind == "NUMERIC" {
		// Exact decimal semantics: MySQL may encode CASE's 0 as "0.00" while
		// COALESCE emits "0". Never pass money/large decimals through float64.
		if len(raw) > 128 {
			return nil, errors.New("invalid_decimal_result")
		}
		value, ok := new(big.Rat).SetString(string(raw))
		if !ok {
			return nil, errors.New("invalid_decimal_result")
		}
		return []byte(value.RatString()), nil
	}
	if kind == "JSON" || kind == "JSONB" {
		var v any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&v) != nil || d.Decode(new(any)) != io.EOF {
			return nil, errors.New("invalid_json_result")
		}
		return json.Marshal(v)
	}
	if strings.Contains(kind, "TIMESTAMP") || strings.Contains(kind, "DATETIME") || kind == "DATE" || kind == "TIMESTAMPTZ" {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02"} {
			if t, e := time.Parse(layout, string(raw)); e == nil {
				if kind == "DATE" {
					return []byte(t.Format("2006-01-02")), nil
				}
				return []byte(t.UTC().Format(time.RFC3339Nano)), nil
			}
		}
		return nil, errors.New("invalid_temporal_result")
	}
	return raw, nil
}
func Read(ctx context.Context, db DB, query string, ordered bool) (string, time.Duration, error) {
	step, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	start := time.Now()
	rows, e := db.QueryContext(step, query)
	if e != nil {
		return "", 0, e
	}
	defer rows.Close()
	columns, e := rows.Columns()
	if e != nil || len(columns) > 128 {
		return "", 0, errors.New("result_column_limit")
	}
	types, e := rows.ColumnTypes()
	if e != nil {
		return "", 0, e
	}
	values := make([]sql.RawBytes, len(columns))
	pointers := make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	hashes := []string{}
	total := 0
	for rows.Next() {
		if e = rows.Scan(pointers...); e != nil {
			return "", 0, e
		}
		h := sha256.New()
		for i, value := range values {
			total += len(value)
			if value == nil {
				h.Write([]byte{0})
				continue
			}
			value, e = Canonical(value, types[i].DatabaseTypeName())
			if e != nil {
				return "", 0, e
			}
			h.Write([]byte{1})
			var size [8]byte
			binary.BigEndian.PutUint64(size[:], uint64(len(value)))
			h.Write(size[:])
			h.Write(value)
		}
		hashes = append(hashes, hex.EncodeToString(h.Sum(nil)))
		if len(hashes) > 10000 || total > 8<<20 {
			return "", 0, errors.New("result_limit_exceeded")
		}
	}
	if e = rows.Err(); e != nil {
		return "", 0, e
	}
	elapsed := time.Since(start)
	if !ordered {
		sort.Strings(hashes)
	}
	return Digest([]byte(strings.Join(columns, "\x00") + "\n" + strings.Join(hashes, "\n"))), elapsed, nil
}
