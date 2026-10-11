// Package benchmark runs bounded read workloads only on isolated shadows.
package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"sqlsentinel/internal/measure"
	"sqlsentinel/internal/resultset"
	"sqlsentinel/internal/shadowdata"
	"sqlsentinel/internal/sqladmit"
)

const ProtocolVersion = "bounded-ab.v1"

type Representative struct {
	DatasetID      string    `json:"dataset_id"`
	SnapshotSHA256 string    `json:"snapshot_sha256"`
	WorkloadSHA256 string    `json:"workload_sha256"`
	ApprovedBy     string    `json:"approved_by"`
	Redacted       bool      `json:"redacted"`
	ExpiresAt      time.Time `json:"expires_at"`
}
type Side struct {
	RawMS      []float64 `json:"raw_ms"`
	MedianMS   float64   `json:"median_ms"`
	P95MS      float64   `json:"p95_ms"`
	NoiseRatio float64   `json:"noise_ratio"`
}
type Report struct {
	Protocol                                  string   `json:"protocol"`
	EvidenceLevel                             string   `json:"evidence_level"`
	Eligible                                  bool     `json:"eligible_for_performance_claim"`
	ResultEquivalent                          bool     `json:"result_equivalent"`
	RejectionReasons                          []string `json:"rejection_reasons"`
	BaselineCalibration, CandidateCalibration Side
	BaselineMeasurement, CandidateMeasurement Side
	Delta                                     float64 `json:"relative_delta"`
	Threshold                                 float64 `json:"significance_threshold"`
	BaselineAnalyze, CandidateAnalyze         string
	WorkloadSHA256                            string     `json:"workload_sha256"`
	SnapshotSHA256                            string     `json:"snapshot_sha256"`
	RepresentativeDataset                     string     `json:"representative_dataset,omitempty"`
	Recommendation                            string     `json:"recommendation"`
	WriteCost                                 *WriteCost `json:"write_cost,omitempty"`
}

func Run(ctx context.Context, baseline, candidateDB shadowdata.DB, query string, tables []string, snapshotSHA string, representative *Representative) (Report, error) {
	return RunQueries(ctx, baseline, candidateDB, query, query, tables, snapshotSHA, representative)
}
func RunQueries(ctx context.Context, baseline, candidateDB shadowdata.DB, query, candidateQuery string, tables []string, snapshotSHA string, representative *Representative) (Report, error) {
	// Never compare a sorted multiset against a semantically ordered result.
	if !sqladmit.Admit(query).Accepted || !sqladmit.Admit(candidateQuery).Accepted || sqladmit.OrderSensitive(query) != sqladmit.OrderSensitive(candidateQuery) {
		return Report{}, errors.New("benchmark_query_rejected")
	}
	r := Report{Protocol: ProtocolVersion, EvidenceLevel: "L1", RejectionReasons: []string{}, WorkloadSHA256: Digest([]byte(query)), SnapshotSHA256: snapshotSHA, Recommendation: "requires_write_cost_validation_and_human_approval"}
	if candidateQuery != query {
		r.WorkloadSHA256 = Digest([]byte(query + "\x00" + candidateQuery))
		r.Recommendation = "snapshot_rewrite_evidence_requires_human_review"
	}
	bResult, _, err := execute(ctx, baseline, query)
	if err != nil {
		return r, err
	}
	cResult, _, err := execute(ctx, candidateDB, candidateQuery)
	if err != nil {
		return r, err
	}
	r.ResultEquivalent = bResult == cResult
	if !r.ResultEquivalent {
		r.RejectionReasons = append(r.RejectionReasons, "result_set_not_equivalent")
		return r, nil
	}
	var bc, cc, bm, cm []time.Duration
	for i := 0; i < 2; i++ {
		if _, _, err = execute(ctx, baseline, query); err != nil {
			return r, err
		}
		if _, _, err = execute(ctx, candidateDB, candidateQuery); err != nil {
			return r, err
		}
	}
	// Twenty independent noise rounds precede five formal rounds. Alternate
	// which side runs first to avoid a persistent warmup/scheduling bias.
	for i := 0; i < 25; i++ {
		var b, c time.Duration
		one := func(db shadowdata.DB, q string) (time.Duration, error) {
			var total time.Duration
			for j := 0; j < 5; j++ {
				_, elapsed, e := execute(ctx, db, q)
				if e != nil {
					return 0, e
				}
				total += elapsed
			}
			return total / 5, nil
		}
		if i%2 == 0 {
			b, err = one(baseline, query)
			if err == nil {
				c, err = one(candidateDB, candidateQuery)
			}
		} else {
			c, err = one(candidateDB, candidateQuery)
			if err == nil {
				b, err = one(baseline, query)
			}
		}
		if err != nil {
			return r, err
		}
		if i < 20 {
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
	bStats, cStats := measure.Summarize("baseline", bm), measure.Summarize("candidate", cm)
	bStats.NoiseRatio = measure.NoiseRatio(bc)
	cStats.NoiseRatio = measure.NoiseRatio(cc)
	decision := measure.Decide(bStats, cStats)
	admission := measure.Admit(measure.Summarize("baseline", bc), measure.Summarize("candidate", cc), &decision)
	r.Delta = decision.RelativeDelta
	r.Threshold = decision.ThresholdRatio
	r.Eligible = admission.EligibleForPerformanceClaim
	r.EvidenceLevel = string(admission.EvidenceLevel)
	for _, reason := range admission.RejectionReasons {
		r.RejectionReasons = append(r.RejectionReasons, string(reason))
	}
	bResult, _, err = execute(ctx, baseline, query)
	if err != nil {
		return r, err
	}
	cResult, _, err = execute(ctx, candidateDB, candidateQuery)
	if err != nil {
		return r, err
	}
	bSnap, err := shadowdata.Compute(ctx, baseline, tables)
	if err != nil {
		return r, err
	}
	cSnap, err := shadowdata.Compute(ctx, candidateDB, tables)
	if err != nil {
		return r, err
	}
	if bResult != cResult || bSnap.Digest != snapshotSHA || cSnap.Digest != snapshotSHA {
		r.Eligible = false
		r.EvidenceLevel = "L1"
		r.ResultEquivalent = false
		r.RejectionReasons = append(r.RejectionReasons, "snapshot_or_result_changed")
	}
	r.BaselineAnalyze, err = analyze(ctx, baseline, query)
	if err != nil {
		return r, err
	}
	r.CandidateAnalyze, err = analyze(ctx, candidateDB, candidateQuery)
	if err != nil {
		return r, err
	}
	Qualify(&r, representative, time.Now())
	return r, nil
}

func Qualify(r *Report, p *Representative, now time.Time) {
	if p == nil {
		return
	}
	if p.DatasetID == "" || p.ApprovedBy == "" || !p.Redacted || !p.ExpiresAt.After(now) || p.SnapshotSHA256 != r.SnapshotSHA256 || p.WorkloadSHA256 != r.WorkloadSHA256 {
		r.RejectionReasons = append(r.RejectionReasons, "representative_manifest_not_bound")
		return
	}
	if r.Eligible && r.ResultEquivalent {
		r.EvidenceLevel = "L3"
		r.RepresentativeDataset = p.DatasetID
	}
}

// Preserve ORDER BY row order; unordered rows use a duplicate-sensitive multiset.
func execute(ctx context.Context, db shadowdata.DB, query string) (string, time.Duration, error) {
	return resultset.Read(ctx, db, query, sqladmit.OrderSensitive(query))
}
func analyze(ctx context.Context, db shadowdata.DB, query string) (string, error) {
	step, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := db.QueryContext(step, "EXPLAIN ANALYZE "+query)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var out strings.Builder
	for rows.Next() {
		var line string
		if err = rows.Scan(&line); err != nil {
			return "", err
		}
		out.WriteString(line)
		out.WriteByte('\n')
		if out.Len() > 128<<10 {
			return "", errors.New("analyze_limit")
		}
	}
	return out.String(), rows.Err()
}
func Digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func summarize(samples []time.Duration) Side {
	out := Side{NoiseRatio: measure.NoiseRatio(samples), MedianMS: float64(measure.Median(samples)) / float64(time.Millisecond)}
	for _, s := range samples {
		out.RawMS = append(out.RawMS, float64(s)/float64(time.Millisecond))
	}
	ordered := append([]float64(nil), out.RawMS...)
	sort.Float64s(ordered)
	out.P95MS = ordered[int(math.Ceil(.95*float64(len(ordered))))-1]
	return out
}
