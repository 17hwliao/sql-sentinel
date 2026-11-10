package shadowdata

import (
	"context"
	"database/sql"
	"errors"
)

type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable string `json:"nullable"`
}
type Index struct {
	Name       string `json:"name"`
	Column     string `json:"column"`
	Expression string `json:"expression,omitempty"`
	Sequence   int    `json:"sequence"`
	Unique     bool   `json:"unique"`
}
type Table struct {
	Name          string   `json:"name"`
	Columns       []Column `json:"columns"`
	Indexes       []Index  `json:"indexes"`
	EstimatedRows int64    `json:"estimated_rows"`
}

// Describe exposes only the explicitly allowed current schema and bounds model
// evidence independently of a caller-controlled SQL string.
func Describe(ctx context.Context, db DB, tables []string) ([]Table, error) {
	if len(tables) > 16 {
		return nil, errors.New("schema_table_limit")
	}
	out := []Table{}
	for _, name := range tables {
		t := Table{Name: name}
		if _, err := Quote(name); err != nil {
			return nil, err
		}
		rows, err := db.QueryContext(ctx, "SELECT COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY ORDINAL_POSITION LIMIT 129", name)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var c Column
			if err = rows.Scan(&c.Name, &c.Type, &c.Nullable); err != nil {
				rows.Close()
				return nil, err
			}
			t.Columns = append(t.Columns, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if len(t.Columns) == 0 || len(t.Columns) > 128 {
			return nil, errors.New("schema_column_limit")
		}
		rows, err = db.QueryContext(ctx, "SELECT INDEX_NAME,COLUMN_NAME,EXPRESSION,SEQ_IN_INDEX,NON_UNIQUE FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY INDEX_NAME,SEQ_IN_INDEX LIMIT 129", name)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var index Index
			var column, expression sql.NullString
			var nonUnique int
			if err = rows.Scan(&index.Name, &column, &expression, &index.Sequence, &nonUnique); err != nil {
				rows.Close()
				return nil, err
			}
			// MySQL functional key parts have NULL COLUMN_NAME and expose their
			// definition through EXPRESSION. Keep it as schema evidence rather
			// than inventing a physical column the candidate compiler can use.
			if !column.Valid && !expression.Valid {
				rows.Close()
				return nil, errors.New("schema_index_metadata_invalid")
			}
			index.Column = column.String
			index.Expression = expression.String
			index.Unique = nonUnique == 0
			t.Indexes = append(t.Indexes, index)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if len(t.Indexes) > 128 {
			return nil, errors.New("schema_index_limit")
		}
		if err = db.QueryRowContext(ctx, "SELECT COALESCE(TABLE_ROWS,0) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND TABLE_TYPE='BASE TABLE'", name).Scan(&t.EstimatedRows); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}
