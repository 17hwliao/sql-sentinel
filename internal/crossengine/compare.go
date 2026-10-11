// Package crossengine imports a bounded MySQL shadow into a disposable PG
// schema and compares the identical admitted query. It never accepts DML.
package crossengine

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sqlsentinel/internal/experimentlock"
	"sqlsentinel/internal/measure"
	"sqlsentinel/internal/resultset"
	"sqlsentinel/internal/shadowdata"
	"sqlsentinel/internal/sqladmit"
	"strings"
	"time"
)

var ErrUnsupported = errors.New("crossengine_schema_or_query_unsupported")
var ident = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,63}$`)
var fixedText = regexp.MustCompile(`^(?:var)?char\([0-9]{1,5}\)$`)
var numeric = regexp.MustCompile(`^decimal\([0-9]{1,2},[0-9]{1,2}\)$`)
var integerType = regexp.MustCompile(`^(bigint|int|smallint|tinyint)(?:\([0-9]{1,2}\))?$`)

func quote(s string) (string, error) {
	if !ident.MatchString(s) {
		return "", ErrUnsupported
	}
	return `"` + s + `"`, nil
}
func pgType(raw string) (string, error) {
	raw = strings.ToLower(raw)
	unsigned := strings.HasSuffix(raw, " unsigned")
	base := strings.TrimSuffix(raw, " unsigned")
	if (strings.HasPrefix(base, "bigint") || strings.HasPrefix(base, "int") || strings.HasPrefix(base, "smallint") || strings.HasPrefix(base, "tinyint")) && !integerType.MatchString(base) {
		return "", ErrUnsupported
	}
	if strings.HasPrefix(base, "bigint") {
		if unsigned {
			return "numeric(20,0)", nil
		}
		return "bigint", nil
	}
	if strings.HasPrefix(base, "int") {
		if unsigned {
			return "bigint", nil
		}
		return "integer", nil
	}
	if strings.HasPrefix(base, "smallint") {
		return "integer", nil
	}
	if strings.HasPrefix(base, "tinyint") {
		return "smallint", nil
	}
	if fixedText.MatchString(base) {
		return base + ` COLLATE "C"`, nil
	}
	if numeric.MatchString(base) {
		return strings.Replace(base, "decimal", "numeric", 1), nil
	}
	switch base {
	case "text", "mediumtext", "longtext", "tinytext":
		return `text COLLATE "C"`, nil
	case "datetime", "datetime(6)", "timestamp", "timestamp(6)":
		return "timestamp(6) without time zone", nil
	case "date":
		return "date", nil
	case "json":
		return "json", nil
	}
	return "", ErrUnsupported
}

type Column struct {
	Name       string `json:"name"`
	SourceType string `json:"source_type"`
	PGType     string `json:"pg_type"`
	Nullable   bool   `json:"nullable"`
	Collation  string `json:"source_collation"`
}
type Table struct {
	Name     string   `json:"name"`
	Columns  []Column `json:"columns"`
	Rows     int      `json:"rows"`
	DataSHA  string   `json:"data_sha256"`
	IndexDDL []string `json:"mapped_index_ddl"`
}
type Report struct {
	Protocol          string            `json:"protocol"`
	QuerySHA          string            `json:"query_sha256"`
	SourceSnapshotSHA string            `json:"source_snapshot_sha256"`
	Tables            []Table           `json:"logical_schema_mapping"`
	DataEquivalent    bool              `json:"data_equivalent"`
	ResultEquivalent  bool              `json:"result_equivalent"`
	MySQLVersion      string            `json:"mysql_version"`
	PGVersion         string            `json:"postgres_version"`
	MySQLSettings     map[string]string `json:"mysql_settings"`
	PGSettings        map[string]string `json:"postgres_settings"`
	MySQLCalibration  measure.SideStats `json:"mysql_calibration"`
	PGCalibration     measure.SideStats `json:"postgres_calibration"`
	MySQLMeasurement  measure.SideStats `json:"mysql_measurement"`
	PGMeasurement     measure.SideStats `json:"postgres_measurement"`
	MySQLRawMS        []float64         `json:"mysql_raw_ms"`
	PGRawMS           []float64         `json:"postgres_raw_ms"`
	Decision          *measure.Decision `json:"decision,omitempty"`
	EvidenceLevel     string            `json:"evidence_level"`
	PerformanceClaim  bool              `json:"eligible_for_experiment_claim"`
	MySQLAnalyze      string            `json:"mysql_analyze"`
	PGAnalyze         json.RawMessage   `json:"postgres_analyze"`
	Limitations       []string          `json:"limitations"`
}

func Run(ctx context.Context, mysql, pg *sql.DB, query string) (report Report, err error) {
	report = Report{Protocol: "crossengine-copy-ab.v1", QuerySHA: resultset.Digest([]byte(query)), EvidenceLevel: "L1", Limitations: []string{"Logical type/index mapping; PostgreSQL C collation may differ from MySQL collation. Equality is checked on this snapshot, not a universal SQL proof.", "Independent engine planner/buffer settings are recorded. This experiment does not qualify production indexing, writes, or L3."}}
	tables, e := sqladmit.Inspect(query)
	if e != nil || len(tables) == 0 || len(tables) > 16 || strings.Contains(query, "`") {
		return report, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	source, e := mysql.Conn(ctx)
	if e != nil {
		return report, e
	}
	defer source.Close()
	release, e := experimentlock.Acquire(ctx, source, "sentinel-crossengine-experiment")
	if e != nil {
		return report, e
	}
	defer release()
	target, e := pg.Conn(ctx)
	if e != nil {
		return report, e
	}
	defer target.Close()
	var database string
	var super bool
	if target.QueryRowContext(ctx, "SELECT current_database(),usesuper FROM pg_user WHERE usename=current_user").Scan(&database, &super) != nil || database != "sentinel_compare" || super {
		return report, errors.New("isolated_non_superuser_postgres_required")
	}
	if _, e = source.ExecContext(ctx, "SET SESSION max_execution_time=2000"); e != nil {
		return report, e
	}
	if _, e = source.ExecContext(ctx, "SET SESSION time_zone='+00:00'"); e != nil {
		return report, e
	}
	if _, e = target.ExecContext(ctx, "SET statement_timeout='2s'"); e != nil {
		return report, e
	}
	if _, e = target.ExecContext(ctx, "SET timezone='UTC'"); e != nil {
		return report, e
	}
	if e = cleanOrphans(ctx, target); e != nil {
		return report, e
	}
	sourceTx, e := source.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return report, e
	}
	defer sourceTx.Rollback()
	random := make([]byte, 12)
	if _, e = rand.Read(random); e != nil {
		return report, e
	}
	schema := "ss_compare_" + hex.EncodeToString(random)
	qSchema, _ := quote(schema)
	if _, e = target.ExecContext(ctx, "CREATE SCHEMA "+qSchema); e != nil {
		return report, e
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		_, cleanErr := target.ExecContext(cleanup, "DROP SCHEMA "+qSchema+" CASCADE")
		if cleanErr != nil {
			err = errors.New("crossengine_cleanup_failed")
		}
	}()
	if _, e = target.ExecContext(ctx, "COMMENT ON SCHEMA "+qSchema+" IS '"+time.Now().UTC().Format(time.RFC3339)+"'"); e != nil {
		return report, e
	}
	if _, e = target.ExecContext(ctx, "SET search_path TO "+qSchema); e != nil {
		return report, e
	}
	totalRows := 0
	for _, table := range tables {
		if !ident.MatchString(table) {
			return report, ErrUnsupported
		}
		var count int
		if e = sourceTx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+table+"`").Scan(&count); e != nil {
			return report, e
		}
		totalRows += count
		if count > 10000 || totalRows > 20000 {
			return report, ErrUnsupported
		}
	}
	before, e := shadowdata.Compute(ctx, sourceTx, tables)
	if e != nil {
		return report, e
	}
	report.SourceSnapshotSHA = before.Digest
	totalBytes, totalRows := 0, 0
	for _, table := range tables {
		t, e := copyTable(ctx, sourceTx, target, table, &totalBytes, &totalRows)
		if e != nil {
			return report, e
		}
		report.Tables = append(report.Tables, t)
	}
	report.DataEquivalent = true
	for _, t := range report.Tables {
		old, _, e := resultset.Read(ctx, sourceTx, "SELECT * FROM `"+t.Name+"`", false)
		if e != nil {
			return report, e
		}
		fresh, _, e := resultset.Read(ctx, target, `SELECT * FROM "`+t.Name+`"`, false)
		if e != nil || old != fresh {
			report.DataEquivalent = false
			return report, errors.New("crossengine_copy_mismatch")
		}
	}
	ordered := sqladmit.OrderSensitive(query)
	old, _, e := resultset.Read(ctx, sourceTx, query, ordered)
	if e != nil {
		return report, e
	}
	fresh, _, e := resultset.Read(ctx, target, query, ordered)
	if e != nil {
		return report, ErrUnsupported
	}
	report.ResultEquivalent = old == fresh
	if !report.ResultEquivalent {
		return report, nil
	}
	sourceTx.QueryRowContext(ctx, "SELECT VERSION()").Scan(&report.MySQLVersion)
	target.QueryRowContext(ctx, "SHOW server_version").Scan(&report.PGVersion)
	report.MySQLSettings = map[string]string{}
	report.PGSettings = map[string]string{}
	for _, setting := range []string{"innodb_buffer_pool_size", "optimizer_switch", "sql_mode"} {
		var v string
		if sourceTx.QueryRowContext(ctx, "SELECT @@"+setting).Scan(&v) == nil {
			report.MySQLSettings[setting] = v
		}
	}
	for _, setting := range []string{"shared_buffers", "work_mem", "effective_cache_size", "random_page_cost"} {
		var v string
		if target.QueryRowContext(ctx, "SHOW "+setting).Scan(&v) == nil {
			report.PGSettings[setting] = v
		}
	}
	var bc, pc, bm, pm []time.Duration
	for round := 0; round < 27; round++ {
		sample := func(db resultset.DB) (time.Duration, error) {
			var elapsed time.Duration
			for i := 0; i < 5; i++ {
				_, d, e := resultset.Read(ctx, db, query, ordered)
				if e != nil {
					return 0, e
				}
				elapsed += d
			}
			return elapsed / 5, nil
		}
		var b, p time.Duration
		if round%2 == 0 {
			b, e = sample(sourceTx)
			if e == nil {
				p, e = sample(target)
			}
		} else {
			p, e = sample(target)
			if e == nil {
				b, e = sample(sourceTx)
			}
		}
		if e != nil {
			return report, e
		}
		if round < 2 {
			continue
		}
		if round < 22 {
			bc = append(bc, b)
			pc = append(pc, p)
		} else {
			bm = append(bm, b)
			pm = append(pm, p)
		}
	}
	report.MySQLCalibration = measure.Summarize("mysql", bc)
	report.PGCalibration = measure.Summarize("postgres", pc)
	report.MySQLMeasurement = measure.Summarize("mysql", bm)
	report.PGMeasurement = measure.Summarize("postgres", pm)
	report.MySQLMeasurement.NoiseRatio = measure.NoiseRatio(bc)
	report.PGMeasurement.NoiseRatio = measure.NoiseRatio(pc)
	decision := measure.Decide(report.MySQLMeasurement, report.PGMeasurement)
	report.Decision = &decision
	admission := measure.Admit(report.MySQLCalibration, report.PGCalibration, &decision)
	report.PerformanceClaim = admission.EligibleForPerformanceClaim
	report.EvidenceLevel = string(admission.EvidenceLevel)
	for _, v := range bm {
		report.MySQLRawMS = append(report.MySQLRawMS, float64(v)/float64(time.Millisecond))
	}
	for _, v := range pm {
		report.PGRawMS = append(report.PGRawMS, float64(v)/float64(time.Millisecond))
	}
	rows, e := sourceTx.QueryContext(ctx, "EXPLAIN ANALYZE "+query)
	if e != nil {
		return report, e
	}
	var analyze strings.Builder
	for rows.Next() {
		var text string
		if e = rows.Scan(&text); e != nil {
			rows.Close()
			return report, e
		}
		analyze.WriteString(text + "\n")
		if analyze.Len() > 128<<10 {
			rows.Close()
			return report, ErrUnsupported
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return report, e
	}
	report.MySQLAnalyze = analyze.String()
	var pgPlan []byte
	if e = target.QueryRowContext(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+query).Scan(&pgPlan); e != nil || len(pgPlan) > 128<<10 {
		return report, ErrUnsupported
	}
	report.PGAnalyze = pgPlan
	if e = sourceTx.Commit(); e != nil {
		return report, e
	}
	after, e := shadowdata.Compute(ctx, source, tables)
	if e != nil || after.Digest != before.Digest {
		return report, errors.New("crossengine_snapshot_changed")
	}
	return report, nil
}

func copyTable(ctx context.Context, source *sql.Tx, target *sql.Conn, name string, totalBytes, totalRows *int) (Table, error) {
	t := Table{Name: name}
	quoted, e := quote(name)
	if e != nil {
		return t, e
	}
	var foreignKeys int
	if source.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=DATABASE() AND TABLE_NAME=?", name).Scan(&foreignKeys) != nil || foreignKeys != 0 {
		return t, ErrUnsupported
	}
	meta, e := source.QueryContext(ctx, "SELECT COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,EXTRA,COALESCE(COLLATION_NAME,'') FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY ORDINAL_POSITION", name)
	if e != nil {
		return t, e
	}
	definitions, names := []string{}, []string{}
	for meta.Next() {
		var c Column
		var nullable, extra string
		if e = meta.Scan(&c.Name, &c.SourceType, &nullable, &extra, &c.Collation); e != nil {
			meta.Close()
			return t, e
		}
		if strings.Contains(extra, "GENERATED") {
			meta.Close()
			return t, ErrUnsupported
		}
		n, e := quote(c.Name)
		if e != nil {
			meta.Close()
			return t, e
		}
		c.PGType, e = pgType(c.SourceType)
		if e != nil {
			meta.Close()
			return t, e
		}
		c.Nullable = nullable == "YES"
		d := n + " " + c.PGType
		if !c.Nullable {
			d += " NOT NULL"
		}
		definitions = append(definitions, d)
		names = append(names, n)
		t.Columns = append(t.Columns, c)
	}
	e = meta.Err()
	meta.Close()
	if e != nil || len(names) == 0 || len(names) > 128 {
		return t, ErrUnsupported
	}
	indexes, e := source.QueryContext(ctx, "SELECT INDEX_NAME,NON_UNIQUE,COLUMN_NAME,COLLATION,SUB_PART,EXPRESSION,INDEX_TYPE FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND LEFT(INDEX_NAME,9)<>'idx_cand_' ORDER BY INDEX_NAME,SEQ_IN_INDEX", name)
	if e != nil {
		return t, e
	}
	type idx struct {
		unique  bool
		columns []string
	}
	group := map[string]*idx{}
	for indexes.Next() {
		var id, col, dir, kind string
		var nonUnique int
		var sub sql.NullInt64
		var expression sql.NullString
		if e = indexes.Scan(&id, &nonUnique, &col, &dir, &sub, &expression, &kind); e != nil {
			indexes.Close()
			return t, e
		}
		if sub.Valid || expression.Valid || kind != "BTREE" {
			indexes.Close()
			return t, ErrUnsupported
		}
		column, e := quote(col)
		if e != nil {
			indexes.Close()
			return t, e
		}
		if group[id] == nil {
			group[id] = &idx{unique: nonUnique == 0}
		}
		if dir == "D" {
			column += " DESC"
		}
		group[id].columns = append(group[id].columns, column)
	}
	e = indexes.Err()
	indexes.Close()
	if e != nil || group["PRIMARY"] == nil || len(group) > 32 {
		return t, ErrUnsupported
	}
	pk := []string{}
	for _, col := range group["PRIMARY"].columns {
		pk = append(pk, strings.TrimSuffix(col, " DESC"))
	}
	definitions = append(definitions, "PRIMARY KEY ("+strings.Join(pk, ",")+")")
	if _, e = target.ExecContext(ctx, "CREATE TABLE "+quoted+"("+strings.Join(definitions, ",")+")"); e != nil {
		return t, ErrUnsupported
	}
	ids := []string{}
	for id := range group {
		if id != "PRIMARY" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		safe := resultset.Digest([]byte(name + id))[:24]
		prefix := "CREATE INDEX "
		if group[id].unique {
			prefix = "CREATE UNIQUE INDEX "
		}
		ddl := prefix + `"idx_` + safe + `" ON ` + quoted + " (" + strings.Join(group[id].columns, ",") + ")"
		if _, e = target.ExecContext(ctx, ddl); e != nil {
			return t, ErrUnsupported
		}
		t.IndexDDL = append(t.IndexDDL, ddl)
	}
	rows, e := source.QueryContext(ctx, "SELECT * FROM `"+name+"` LIMIT 10001")
	if e != nil {
		return t, e
	}
	types, e := rows.ColumnTypes()
	if e != nil {
		rows.Close()
		return t, e
	}
	values := make([]sql.RawBytes, len(names))
	ptr := make([]any, len(names))
	for i := range values {
		ptr[i] = &values[i]
	}
	var batch [][]any
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		arguments := []any{}
		tuples := []string{}
		for _, row := range batch {
			slots := []string{}
			for index, value := range row {
				arguments = append(arguments, value)
				slots = append(slots, fmt.Sprintf("CAST($%d::text AS %s)", len(arguments), strings.Split(t.Columns[index].PGType, " COLLATE")[0]))
			}
			tuples = append(tuples, "("+strings.Join(slots, ",")+")")
		}
		_, e := target.ExecContext(ctx, "INSERT INTO "+quoted+"("+strings.Join(names, ",")+") VALUES "+strings.Join(tuples, ","), arguments...)
		batch = nil
		return e
	}
	for rows.Next() {
		if e = rows.Scan(ptr...); e != nil {
			rows.Close()
			return t, e
		}
		t.Rows++
		*totalRows++
		copyRow := make([]any, len(values))
		for i, value := range values {
			*totalBytes += len(value)
			if value != nil {
				canonical, e := resultset.Canonical(value, types[i].DatabaseTypeName())
				if e != nil {
					rows.Close()
					return t, e
				}
				copyRow[i] = string(canonical)
			}
		}
		if t.Rows > 10000 || *totalRows > 20000 || *totalBytes > 32<<20 {
			rows.Close()
			return t, errors.New("crossengine_copy_limit")
		}
		batch = append(batch, copyRow)
		if len(batch) == 100 {
			if e = flush(); e != nil {
				rows.Close()
				return t, ErrUnsupported
			}
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return t, e
	}
	if e = flush(); e != nil {
		return t, ErrUnsupported
	}
	if _, e = target.ExecContext(ctx, "ANALYZE "+quoted); e != nil {
		return t, e
	}
	t.DataSHA, _, e = resultset.Read(ctx, source, "SELECT * FROM `"+name+"`", false)
	return t, e
}
func cleanOrphans(ctx context.Context, target *sql.Conn) error {
	rows, e := target.QueryContext(ctx, "SELECT nspname,COALESCE(obj_description(oid,'pg_namespace'),'') FROM pg_namespace WHERE nspowner=(SELECT oid FROM pg_roles WHERE rolname=current_user) AND nspname LIKE 'ss_compare_%' LIMIT 100")
	if e != nil {
		return e
	}
	var names []string
	for rows.Next() {
		var name, created string
		if e = rows.Scan(&name, &created); e != nil {
			rows.Close()
			return e
		}
		if len(name) != 35 || !strings.HasPrefix(name, "ss_compare_") {
			continue
		}
		if _, e = hex.DecodeString(strings.TrimPrefix(name, "ss_compare_")); e != nil {
			continue
		}
		t, e := time.Parse(time.RFC3339, created)
		if e == nil && t.Before(time.Now().Add(-5*time.Minute)) {
			names = append(names, name)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, name := range names {
		quoted, e := quote(name)
		if e != nil {
			return e
		}
		if _, e = target.ExecContext(ctx, "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			return e
		}
	}
	return nil
}
