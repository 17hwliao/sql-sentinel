package benchmark

import (
	"context"
	"errors"
	"sqlsentinel/internal/measure"
	"sqlsentinel/internal/shadowdata"
	"time"
)

// WriteCost measures a fixed insert workload on connection-local temporary
// copies. It never mutates the imported shadow rows, runs triggers, or purports
// to replay the production write workload.
type WriteCost struct {
	Protocol             string   `json:"protocol"`
	Table                string   `json:"table"`
	Rows                 int64    `json:"rows_per_round"`
	BaselineCalibration  Side     `json:"baseline_calibration"`
	CandidateCalibration Side     `json:"candidate_calibration"`
	BaselineMeasurement  Side     `json:"baseline_measurement"`
	CandidateMeasurement Side     `json:"candidate_measurement"`
	RelativeOverhead     float64  `json:"relative_overhead"`
	MaxOverhead          float64  `json:"max_overhead"`
	Eligible             bool     `json:"eligible"`
	Reasons              []string `json:"rejection_reasons"`
}

func ProbeWrites(ctx context.Context, baseline, candidate shadowdata.DB, table string) (r WriteCost, err error) {
	r = WriteCost{Protocol: "temporary-insert-256.v1", Table: table, MaxOverhead: .20, Reasons: []string{}}
	q, err := shadowdata.Quote(table)
	if err != nil {
		return r, err
	}
	const probe = "`sentinel_write_probe`"
	// The caller supplies dedicated connections. A failed cleanup poisons the
	// connection at the caller boundary so a surviving temporary table cannot
	// enter a later job's measurement.
	for _, db := range []shadowdata.DB{baseline, candidate} {
		if _, err = db.ExecContext(ctx, "DROP TEMPORARY TABLE IF EXISTS "+probe); err != nil {
			return r, err
		}
		if _, err = db.ExecContext(ctx, "CREATE TEMPORARY TABLE "+probe+" LIKE "+q); err != nil {
			return r, err
		}
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		for _, db := range []shadowdata.DB{baseline, candidate} {
			if _, e := db.ExecContext(clean, "DROP TEMPORARY TABLE IF EXISTS "+probe); e != nil {
				err = errors.New("write_probe_cleanup_failed")
			}
		}
	}()
	keys, e := baseline.QueryContext(ctx, "SELECT COLUMN_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND INDEX_NAME='PRIMARY' ORDER BY SEQ_IN_INDEX", table)
	if e != nil {
		return r, e
	}
	order := ""
	for keys.Next() {
		var name string
		if e = keys.Scan(&name); e != nil {
			keys.Close()
			return r, e
		}
		col, e := shadowdata.Quote(name)
		if e != nil {
			keys.Close()
			return r, e
		}
		if order != "" {
			order += ","
		}
		order += col
	}
	e = keys.Err()
	keys.Close()
	if e != nil {
		return r, e
	}
	if order == "" {
		return r, errors.New("write_probe_primary_key_required")
	}
	// Explicit writable columns exclude generated columns and hidden fields.
	rows, err := baseline.QueryContext(ctx, "SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND EXTRA NOT LIKE '%GENERATED%' ORDER BY ORDINAL_POSITION", table)
	if err != nil {
		return r, err
	}
	columns := ""
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return r, err
		}
		col, e := shadowdata.Quote(name)
		if e != nil {
			rows.Close()
			return r, e
		}
		if columns != "" {
			columns += ","
		}
		columns += col
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return r, err
	}
	if columns == "" {
		return r, errors.New("write_probe_no_columns")
	}
	var bc, cc, bm, cm []time.Duration
	one := func(db shadowdata.DB) (time.Duration, error) {
		step, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if _, e := db.ExecContext(step, "DELETE FROM "+probe); e != nil {
			return 0, e
		}
		started := time.Now()
		res, e := db.ExecContext(step, "INSERT INTO "+probe+" ("+columns+") SELECT "+columns+" FROM "+q+" ORDER BY "+order+" LIMIT 256")
		elapsed := time.Since(started)
		if e != nil {
			return 0, e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return 0, e
		}
		if r.Rows != 0 && r.Rows != n {
			return 0, errors.New("write_probe_rows_changed")
		}
		r.Rows = n
		return elapsed, nil
	}
	for i := 0; i < 27; i++ {
		var b, c time.Duration
		if i%2 == 0 {
			b, err = one(baseline)
			if err == nil {
				c, err = one(candidate)
			}
		} else {
			c, err = one(candidate)
			if err == nil {
				b, err = one(baseline)
			}
		}
		if err != nil {
			return r, err
		}
		if i < 2 {
			continue
		}
		if i < 22 {
			bc = append(bc, b)
			cc = append(cc, c)
		} else {
			bm = append(bm, b)
			cm = append(cm, c)
		}
	}
	r.BaselineCalibration = summarize(bc)
	r.CandidateCalibration = summarize(cc)
	r.BaselineMeasurement = summarize(bm)
	r.CandidateMeasurement = summarize(cm)
	if r.BaselineMeasurement.MedianMS <= 0 {
		return r, errors.New("write_probe_invalid_time")
	}
	r.RelativeOverhead = r.CandidateMeasurement.MedianMS/r.BaselineMeasurement.MedianMS - 1
	if r.Rows < 32 {
		r.Reasons = append(r.Reasons, "write_probe_insufficient_rows")
	}
	if r.BaselineCalibration.NoiseRatio > measure.NoiseRatioLimit || r.CandidateCalibration.NoiseRatio > measure.NoiseRatioLimit {
		r.Reasons = append(r.Reasons, "write_probe_noise_exceeds_limit")
	}
	if r.RelativeOverhead > r.MaxOverhead {
		r.Reasons = append(r.Reasons, "write_probe_overhead_exceeds_limit")
	}
	r.Eligible = len(r.Reasons) == 0
	return r, nil
}
