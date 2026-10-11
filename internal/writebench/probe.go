// Package writebench runs bounded synthetic write experiments only in an
// explicitly named isolated scratch database. Imported shadow rows are never
// modified. Results do not qualify production workloads or L3 evidence.
package writebench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"regexp"
	"sort"
	"sqlsentinel/internal/candidate"
	"sqlsentinel/internal/experimentlock"
	"sqlsentinel/internal/measure"
	"strings"
	"sync"
	"time"
)

var ErrIsolated = errors.New("isolated_write_fixture_required")

type Round struct {
	WallMS     float64   `json:"wall_ms"`
	P95MS      float64   `json:"p95_ms"`
	Throughput float64   `json:"operations_per_second"`
	LockWaitMS uint64    `json:"server_row_lock_time_ms"`
	RawMS      []float64 `json:"raw_ms"`
}
type Side struct {
	BuildMS      float64 `json:"index_build_ms"`
	IndexBytes   uint64  `json:"estimated_index_bytes"`
	Calibration  []Round `json:"calibration"`
	Measurement  []Round `json:"measurement"`
	Noise        float64 `json:"noise_ratio"`
	MedianWallMS float64 `json:"median_wall_ms"`
	P95MS        float64 `json:"p95_ms"`
	Throughput   float64 `json:"operations_per_second"`
}
type Report struct {
	Protocol      string         `json:"protocol"`
	Evidence      string         `json:"evidence_level"`
	Spec          candidate.Spec `json:"candidate_spec"`
	Baseline      Side           `json:"baseline"`
	Candidate     Side           `json:"candidate"`
	Workers       int            `json:"workers"`
	Ops           int            `json:"operations_per_round"`
	WriteOverhead float64        `json:"relative_write_overhead"`
	Eligible      bool           `json:"eligible_for_fixture_write_claim"`
	Reasons       []string       `json:"rejection_reasons"`
	Limitations   []string       `json:"limitations"`
}

func Validate(spec candidate.Spec) error {
	if candidate.Validate(spec) != nil || spec.Table != "orders" {
		return ErrIsolated
	}
	for _, c := range spec.Columns {
		if c.Name != "user_id" && c.Name != "status" && c.Name != "created_at" {
			return ErrIsolated
		}
	}
	return nil
}
func Run(ctx context.Context, db *sql.DB, spec candidate.Spec) (report Report, err error) {
	report = Report{Protocol: "isolated-concurrent-insert-update.v1", Evidence: "L1", Spec: spec, Workers: 4, Ops: 64, Reasons: []string{}, Limitations: []string{"Deterministic synthetic orders fixture; not a replay of production traffic. No production recommendation or L3 qualification.", "P95 describes individual statement latency; noise is calibrated from wall time. InnoDB row-lock time is a server counter delta on this dedicated scratch server.", "Index size is the MySQL information_schema estimate after ANALYZE. Build time is wall time on 256 fixture rows."}}
	if Validate(spec) != nil {
		return report, ErrIsolated
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var name string
	if db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&name) != nil || name != "sentinel_write" {
		return report, ErrIsolated
	}
	var other int
	if db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME NOT IN ('sentinel_write','information_schema','performance_schema','mysql','sys')").Scan(&other) != nil || other != 0 {
		return report, ErrIsolated
	}
	lockConn, e := db.Conn(ctx)
	if e != nil {
		return report, e
	}
	defer lockConn.Close()
	release, e := experimentlock.Acquire(ctx, lockConn, "sentinel-write-experiment")
	if e != nil {
		return report, e
	}
	defer release()
	if e := cleanOrphans(ctx, db); e != nil {
		return report, e
	}
	token := make([]byte, 12)
	if _, err = rand.Read(token); err != nil {
		return report, err
	}
	names := []string{"sw_" + hex.EncodeToString(token) + "_b", "sw_" + hex.EncodeToString(token) + "_c"}
	// Cleanup is registered before creating either table; no arbitrary DROP is
	// accepted. The exact generated names belong only to this invocation.
	defer func() {
		c, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		for _, name := range names {
			if _, e := db.ExecContext(c, "DROP TABLE IF EXISTS `"+name+"`"); e != nil {
				err = errors.New("write_fixture_cleanup_failed")
			}
		}
	}()
	for _, name := range names {
		_, err = db.ExecContext(ctx, "CREATE TABLE `"+name+"` (id BIGINT PRIMARY KEY,user_id BIGINT NOT NULL,status VARCHAR(16) NOT NULL,created_at DATETIME(6) NOT NULL,payload VARCHAR(256) NOT NULL) ENGINE=InnoDB COMMENT='sentinel-write:"+time.Now().UTC().Format(time.RFC3339)+"'")
		if err != nil {
			return report, err
		}
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return report, e
		}
		for i := 0; i < 256; i++ {
			if _, e = tx.ExecContext(ctx, "INSERT INTO `"+name+"` VALUES (?,?,?, ?,?)", i, i%32, "paid", "2026-01-01 00:00:00", "fixed-payload"); e != nil {
				tx.Rollback()
				return report, e
			}
		}
		if e = tx.Commit(); e != nil {
			return report, e
		}
	}
	candidateSpec := spec
	candidateSpec.Table = names[1]
	ddl, e := candidate.CompileCreateIndex(candidateSpec)
	if e != nil {
		return report, e
	}
	start := time.Now()
	_, e = db.ExecContext(ctx, ddl)
	report.Candidate.BuildMS = float64(time.Since(start)) / float64(time.Millisecond)
	if e != nil {
		return report, e
	}
	for side, name := range names {
		if _, e = db.ExecContext(ctx, "ANALYZE TABLE `"+name+"`"); e != nil {
			return report, e
		}
		var size uint64
		if e = db.QueryRowContext(ctx, "SELECT COALESCE(INDEX_LENGTH,0) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", name).Scan(&size); e != nil {
			return report, e
		}
		if side == 0 {
			report.Baseline.IndexBytes = size
		} else {
			report.Candidate.IndexBytes = size
		}
	}
	for i := 0; i < 27; i++ {
		var b, c Round
		if i%2 == 0 {
			b, e = runRound(ctx, db, names[0], report.Workers)
			if e == nil {
				c, e = runRound(ctx, db, names[1], report.Workers)
			}
		} else {
			c, e = runRound(ctx, db, names[1], report.Workers)
			if e == nil {
				b, e = runRound(ctx, db, names[0], report.Workers)
			}
		}
		if e != nil {
			return report, e
		}
		if i < 2 {
			continue
		}
		if i < 22 {
			report.Baseline.Calibration = append(report.Baseline.Calibration, b)
			report.Candidate.Calibration = append(report.Candidate.Calibration, c)
		} else {
			report.Baseline.Measurement = append(report.Baseline.Measurement, b)
			report.Candidate.Measurement = append(report.Candidate.Measurement, c)
		}
	}
	for _, side := range []*Side{&report.Baseline, &report.Candidate} {
		var noise, formal []time.Duration
		var raw []float64
		for _, r := range side.Calibration {
			noise = append(noise, time.Duration(r.WallMS*float64(time.Millisecond)))
		}
		for _, r := range side.Measurement {
			formal = append(formal, time.Duration(r.WallMS*float64(time.Millisecond)))
			raw = append(raw, r.RawMS...)
		}
		side.Noise = measure.NoiseRatio(noise)
		side.MedianWallMS = float64(measure.Median(formal)) / float64(time.Millisecond)
		side.P95MS = p95(raw)
		side.Throughput = float64(report.Ops) * 1000 / side.MedianWallMS
	}
	report.WriteOverhead = report.Candidate.MedianWallMS/report.Baseline.MedianWallMS - 1
	if report.Baseline.Noise > .10 || report.Candidate.Noise > .10 {
		report.Reasons = append(report.Reasons, "write_noise_exceeds_10_percent")
	}
	if report.WriteOverhead > .20 {
		report.Reasons = append(report.Reasons, "write_overhead_exceeds_20_percent")
	}
	report.Eligible = len(report.Reasons) == 0
	return report, nil
}
func cleanOrphans(ctx context.Context, db *sql.DB) error {
	rows, e := db.QueryContext(ctx, "SELECT TABLE_NAME,TABLE_COMMENT FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME LIKE 'sw_%' LIMIT 100")
	if e != nil {
		return e
	}
	pattern := regexp.MustCompile(`^sw_[a-f0-9]{24}_[bc]$`)
	var stale []string
	for rows.Next() {
		var name, comment string
		if e = rows.Scan(&name, &comment); e != nil {
			rows.Close()
			return e
		}
		created, err := time.Parse(time.RFC3339, strings.TrimPrefix(comment, "sentinel-write:"))
		if pattern.MatchString(name) && strings.HasPrefix(comment, "sentinel-write:") && err == nil && time.Since(created) > 5*time.Minute {
			stale = append(stale, name)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, name := range stale {
		if _, e = db.ExecContext(ctx, "DROP TABLE `"+name+"`"); e != nil {
			return e
		}
	}
	return nil
}
func lockTime(ctx context.Context, db *sql.DB) (uint64, error) {
	var label string
	var value uint64
	e := db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Innodb_row_lock_time'").Scan(&label, &value)
	return value, e
}
func p95(raw []float64) float64 {
	if len(raw) == 0 {
		return 0
	}
	ordered := append([]float64(nil), raw...)
	sort.Float64s(ordered)
	return ordered[int(math.Ceil(.95*float64(len(ordered))))-1]
}
func runRound(ctx context.Context, db *sql.DB, name string, workers int) (Round, error) {
	var r Round
	before, e := lockTime(ctx, db)
	if e != nil {
		return r, e
	}
	start := time.Now()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failure error
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			conn, e := db.Conn(ctx)
			if e != nil {
				mu.Lock()
				failure = e
				mu.Unlock()
				return
			}
			defer conn.Close()
			if _, e = conn.ExecContext(ctx, "SET SESSION innodb_lock_wait_timeout=2"); e != nil {
				mu.Lock()
				failure = e
				mu.Unlock()
				return
			}
			var samples []float64
			for n := 0; n < 16; n++ {
				tx, e := conn.BeginTx(ctx, nil)
				if e != nil {
					mu.Lock()
					failure = e
					mu.Unlock()
					return
				}
				begin := time.Now()
				// Shared row zero deliberately produces real contention. Inserts use
				// distinct keys. Every transaction rolls back, leaving each round equal.
				if n%2 == 0 {
					_, e = tx.ExecContext(ctx, "UPDATE `"+name+"` SET user_id=user_id+1,status='pending' WHERE id=0")
				} else {
					_, e = tx.ExecContext(ctx, "INSERT INTO `"+name+"` VALUES (?,?,?, ?,?)", 10000+worker*100+n, worker, "new", "2026-01-01 00:00:00", "fixed-payload")
				}
				samples = append(samples, float64(time.Since(begin))/float64(time.Millisecond))
				rollback := tx.Rollback()
				if e == nil {
					e = rollback
				}
				if e != nil {
					mu.Lock()
					failure = e
					mu.Unlock()
					return
				}
			}
			mu.Lock()
			r.RawMS = append(r.RawMS, samples...)
			mu.Unlock()
		}(worker)
	}
	wg.Wait()
	if failure != nil {
		return r, failure
	}
	r.WallMS = float64(time.Since(start)) / float64(time.Millisecond)
	r.P95MS = p95(r.RawMS)
	r.Throughput = float64(len(r.RawMS)) * 1000 / r.WallMS
	after, e := lockTime(ctx, db)
	if e != nil || after < before {
		return r, errors.New("write_counter_changed")
	}
	r.LockWaitMS = after - before
	return r, nil
}
