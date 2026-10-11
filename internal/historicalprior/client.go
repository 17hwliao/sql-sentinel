// Package historicalprior retrieves scoped historical hints. These are never
// current shadow measurements and cannot raise the L1/L2/L3 evidence level.
package historicalprior

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sqlsentinel/internal/telemetry"
	"strings"
	"time"
)

const Index = "sentinel-priors-v1"

func DocumentID(jobID string) string {
	sum := sha256.Sum256([]byte(jobID))
	return hex.EncodeToString(sum[:])
}

var ErrUnavailable = errors.New("historical_prior_unavailable")
var name = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var sha = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Document struct {
	ID              string    `json:"id"`
	Tenant          string    `json:"tenant_id"`
	Project         string    `json:"project_id"`
	Table           string    `json:"table"`
	Tables          []string  `json:"tables"`
	Columns         []string  `json:"columns"`
	SQLSHA          string    `json:"sql_sha256"`
	SnapshotSHA     string    `json:"snapshot_sha256,omitempty"`
	Text            string    `json:"text"`
	Level           string    `json:"evidence_level"`
	Measured        bool      `json:"measured"`
	BaselineMedian  float64   `json:"baseline_median_ms"`
	CandidateMedian float64   `json:"candidate_median_ms"`
	Noise           float64   `json:"noise_ratio"`
	Expires         time.Time `json:"expires_at"`
}

func (d Document) Valid(now time.Time) bool {
	if (d.SnapshotSHA != "" && !sha.MatchString(d.SnapshotSHA)) || d.Expires.After(now.Add(31*24*time.Hour)) {
		return false
	}
	if !sha.MatchString(d.ID) || !name.MatchString(d.Tenant) || !name.MatchString(d.Project) || !name.MatchString(d.Table) || len(d.Tables) == 0 || len(d.Tables) > 16 || len(d.Columns) == 0 || len(d.Columns) > 4 || !sha.MatchString(d.SQLSHA) || len(d.Text) > 1024 || !d.Expires.After(now) || (d.Level != "L1" && d.Level != "L2" && d.Level != "L3") {
		return false
	}
	for _, list := range [][]string{d.Tables, d.Columns} {
		for _, v := range list {
			if !name.MatchString(v) {
				return false
			}
		}
	}
	for _, v := range []float64{d.BaselineMedian, d.CandidateMedian, d.Noise} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return false
		}
	}
	return true
}

type Result struct {
	HistoricalOnly         bool       `json:"historical_only"`
	Documents              []Document `json:"documents"`
	Count                  int64      `json:"count"`
	MeasuredCount          int64      `json:"measured_count"`
	MaxReportedNoise       float64    `json:"page_max_reported_noise"`
	MinReportedCandidateMS *float64   `json:"page_min_reported_candidate_ms,omitempty"`
}
type Client struct {
	URL, APIKey string
	HTTP        *http.Client
}

func New(base, key, ca string) (*Client, error) {
	u, e := url.Parse(base)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "https" && u.Scheme != "http") || len(key) < 16 {
		return nil, ErrUnavailable
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if ca != "" {
		raw, e := os.ReadFile(ca)
		if e != nil {
			return nil, ErrUnavailable
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(raw) {
			return nil, ErrUnavailable
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	}
	return &Client{URL: strings.TrimRight(base, "/"), APIKey: key, HTTP: &http.Client{Timeout: 3 * time.Second, Transport: telemetry.Transport{Base: transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) request(ctx context.Context, method, path string, v any) ([]byte, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, method, c.URL+path, bytes.NewReader(raw))
	if e != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "ApiKey "+c.APIKey)
	response, e := c.HTTP.Do(req)
	if e != nil {
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	body, e := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if e != nil || len(body) > 1<<20 || response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, ErrUnavailable
	}
	return body, nil
}
func (c *Client) Put(ctx context.Context, d Document) error {
	if !d.Valid(time.Now()) {
		return ErrUnavailable
	}
	_, e := c.request(ctx, "PUT", "/"+Index+"/_doc/"+d.ID, d)
	return e
}
func (c *Client) Search(ctx context.Context, tenant, project, table, text string, allowed map[string]bool) (Result, error) {
	result := Result{HistoricalOnly: true, Documents: []Document{}}
	if !name.MatchString(tenant) || !name.MatchString(project) || !name.MatchString(table) || len(text) > 512 || (!allowed[table] && !allowed["*"]) {
		return result, ErrUnavailable
	}
	filters := []any{map[string]any{"term": map[string]string{"tenant_id": tenant}}, map[string]any{"term": map[string]string{"project_id": project}}, map[string]any{"term": map[string]string{"table": table}}, map[string]any{"range": map[string]any{"expires_at": map[string]string{"gt": "now"}}}}
	query := map[string]any{"bool": map[string]any{"filter": filters, "must": []any{map[string]any{"match": map[string]string{"text": text}}}}}
	if strings.TrimSpace(text) == "" {
		query["bool"].(map[string]any)["must"] = []any{map[string]any{"match_all": map[string]any{}}}
	}
	payload := map[string]any{"size": 20, "track_total_hits": true, "timeout": "1s", "query": query, "sort": []any{map[string]string{"expires_at": "desc"}}}
	raw, e := c.request(ctx, "POST", "/"+Index+"/_search", payload)
	if e != nil {
		return result, e
	}
	var wire struct {
		TimedOut bool `json:"timed_out"`
		Shards   struct {
			Failed int `json:"failed"`
		} `json:"_shards"`
		Hits struct {
			Hits []struct {
				Source Document `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.TimedOut || wire.Shards.Failed != 0 || len(wire.Hits.Hits) > 20 {
		return result, ErrUnavailable
	}
	for _, hit := range wire.Hits.Hits {
		d := hit.Source
		if !d.Valid(time.Now()) || d.Tenant != tenant || d.Project != project || d.Table != table {
			continue
		}
		authorized := true
		for _, t := range d.Tables {
			if !allowed[t] && !allowed["*"] {
				authorized = false
			}
		}
		if !authorized {
			continue
		}
		result.Documents = append(result.Documents, d)
		result.Count++
		if d.Measured {
			result.MeasuredCount++
			if d.Noise > result.MaxReportedNoise {
				result.MaxReportedNoise = d.Noise
			}
			if result.MinReportedCandidateMS == nil || d.CandidateMedian < *result.MinReportedCandidateMS {
				v := d.CandidateMedian
				result.MinReportedCandidateMS = &v
			}
		}
	}
	// Counts describe only the bounded, revalidated page, avoiding aggregation
	// leakage of other tables and avoiding an unbounded all-history query.
	return result, nil
}
func (c *Client) DeleteExpired(ctx context.Context) error {
	_, e := c.request(ctx, "POST", "/"+Index+"/_delete_by_query?conflicts=proceed&max_docs=1000", map[string]any{"query": map[string]any{"range": map[string]any{"expires_at": map[string]string{"lte": "now"}}}})
	return e
}
func (c *Client) CreateIndex(ctx context.Context) error {
	mapping := map[string]any{"dynamic": "strict", "properties": map[string]any{}}
	props := mapping["properties"].(map[string]any)
	for _, key := range []string{"id", "tenant_id", "project_id", "table", "tables", "columns", "sql_sha256", "snapshot_sha256", "evidence_level"} {
		props[key] = map[string]string{"type": "keyword"}
	}
	props["text"] = map[string]string{"type": "text"}
	props["expires_at"] = map[string]string{"type": "date"}
	props["measured"] = map[string]string{"type": "boolean"}
	for _, key := range []string{"baseline_median_ms", "candidate_median_ms", "noise_ratio"} {
		props[key] = map[string]string{"type": "double"}
	}
	_, e := c.request(ctx, "PUT", "/"+Index, map[string]any{"settings": map[string]int{"number_of_shards": 1, "number_of_replicas": 0}, "mappings": mapping})
	return e
}
