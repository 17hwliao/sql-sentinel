// Package shadowdata compares arbitrary explicitly allowed shadow tables.
package shadowdata

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"regexp"
	"strings"
)

type DB interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}
type Snapshot struct {
	Digest string `json:"digest"`
	Rows   int64  `json:"rows"`
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

func Quote(name string) (string, error) {
	if !identifier.MatchString(name) {
		return "", errors.New("unsafe_identifier")
	}
	return "`" + name + "`", nil
}

// Read data ordered by every PRIMARY KEY column, including composite/string
// keys. Length prefixes and NULL markers prevent separator collisions.
func Compute(ctx context.Context, db DB, tables []string) (Snapshot, error) {
	h := sha256.New()
	var count int64
	bytes := int64(0)
	if len(tables) == 0 || len(tables) > 16 {
		return Snapshot{}, errors.New("shadow_table_limit")
	}
	for _, table := range tables {
		quoted, err := Quote(table)
		if err != nil {
			return Snapshot{}, err
		}
		var kind string
		if err = db.QueryRowContext(ctx, "SELECT TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", table).Scan(&kind); err != nil || kind != "BASE TABLE" {
			return Snapshot{}, errors.New("shadow_base_table_required")
		}
		meta, err := db.QueryContext(ctx, "SELECT COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,COLUMN_DEFAULT,EXTRA,COLLATION_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY ORDINAL_POSITION", table)
		if err != nil {
			return Snapshot{}, err
		}
		h.Write([]byte("table\x00"))
		frame(h, []byte(table), false)
		for meta.Next() {
			h.Write([]byte("column\x00"))
			vals := make([]sql.NullString, 6)
			if err = meta.Scan(&vals[0], &vals[1], &vals[2], &vals[3], &vals[4], &vals[5]); err != nil {
				meta.Close()
				return Snapshot{}, err
			}
			for _, v := range vals {
				frame(h, []byte(v.String), !v.Valid)
			}
		}
		err = meta.Err()
		meta.Close()
		if err != nil {
			return Snapshot{}, err
		}
		keys, err := db.QueryContext(ctx, "SELECT COLUMN_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND INDEX_NAME='PRIMARY' ORDER BY SEQ_IN_INDEX", table)
		if err != nil {
			return Snapshot{}, err
		}
		var order []string
		for keys.Next() {
			var col string
			if err = keys.Scan(&col); err != nil {
				keys.Close()
				return Snapshot{}, err
			}
			q, e := Quote(col)
			if e != nil {
				keys.Close()
				return Snapshot{}, e
			}
			order = append(order, q)
		}
		err = keys.Err()
		keys.Close()
		if err != nil {
			return Snapshot{}, err
		}
		if len(order) == 0 {
			return Snapshot{}, errors.New("shadow_primary_key_required")
		}
		h.Write([]byte("primarykey\x00"))
		for _, col := range order {
			frame(h, []byte(col), false)
		}
		indexes, err := db.QueryContext(ctx, "SELECT INDEX_NAME,NON_UNIQUE,SEQ_IN_INDEX,COLUMN_NAME,COLLATION,SUB_PART,INDEX_TYPE,IS_VISIBLE,EXPRESSION FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND LEFT(INDEX_NAME,9)<>'idx_cand_' ORDER BY INDEX_NAME,SEQ_IN_INDEX", table)
		if err != nil {
			return Snapshot{}, err
		}
		for indexes.Next() {
			h.Write([]byte("index\x00"))
			vals := make([]sql.NullString, 9)
			if err = indexes.Scan(&vals[0], &vals[1], &vals[2], &vals[3], &vals[4], &vals[5], &vals[6], &vals[7], &vals[8]); err != nil {
				indexes.Close()
				return Snapshot{}, err
			}
			for _, value := range vals {
				frame(h, []byte(value.String), !value.Valid)
			}
		}
		err = indexes.Err()
		indexes.Close()
		if err != nil {
			return Snapshot{}, err
		}
		rows, err := db.QueryContext(ctx, "SELECT * FROM "+quoted+" ORDER BY "+strings.Join(order, ",")+" LIMIT 2000001")
		if err != nil {
			return Snapshot{}, err
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return Snapshot{}, err
		}
		vals := make([]sql.RawBytes, len(cols))
		ptr := make([]any, len(cols))
		for i := range vals {
			ptr[i] = &vals[i]
		}
		for rows.Next() {
			if err = rows.Scan(ptr...); err != nil {
				rows.Close()
				return Snapshot{}, err
			}
			count++
			h.Write([]byte("row\x00"))
			for _, v := range vals {
				bytes += int64(len(v))
				frame(h, v, v == nil)
			}
			if count > 2000000 || bytes > 256<<20 {
				rows.Close()
				return Snapshot{}, errors.New("shadow_snapshot_limit")
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return Snapshot{}, err
		}
		h.Write([]byte("endtable\x00"))
	}
	return Snapshot{Digest: hex.EncodeToString(h.Sum(nil)), Rows: count}, nil
}
func frame(h hash.Hash, raw []byte, isNull bool) {
	if isNull {
		h.Write([]byte{0})
		return
	}
	h.Write([]byte{1})
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(raw)))
	h.Write(size[:])
	h.Write(raw)
}
