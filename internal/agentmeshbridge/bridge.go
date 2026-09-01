// Package agentmeshbridge implements a deliberately narrow, local-only
// transport check against AgentMesh's published streaming chat contract.
package agentmeshbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	EvidenceLevel = "L1"

	StatusCompleted   = "verification_completed"
	StatusUnavailable = "verification_unavailable"
	StatusFailed      = "verification_failed"

	ReasonConfigurationMissing = "agentmesh_bridge_configuration_missing"
	ReasonConfigurationInvalid = "agentmesh_bridge_configuration_invalid"
	ReasonAuthenticationFailed = "agentmesh_auth_failed"
	ReasonRateLimited          = "agentmesh_rate_limited"
	ReasonHTTPRejected         = "agentmesh_http_rejected"
	ReasonResponseNotSSE       = "agentmesh_response_not_sse"
	ReasonTraceMissing         = "agentmesh_trace_missing"
	ReasonStreamInterrupted    = "agentmesh_stream_interrupted"
	ReasonStreamIncomplete     = "agentmesh_stream_incomplete"
	ReasonTimeout              = "agentmesh_timeout"
	ReasonRequestFailed        = "agentmesh_request_failed"
)

// ConnectivityPrompt is intentionally neither SQL nor database evidence.
// It is never written to a report or log.
const ConnectivityPrompt = "SQL Sentinel bridge connectivity check. Reply with a short acknowledgement."

// Config contains only the narrow published AgentMesh contract inputs. Secret
// values are intentionally excluded from JSON serialization.
type Config struct {
	BaseURL            string `json:"-"`
	APIKey             string `json:"-"`
	Model              string `json:"-"`
	AgentMeshCommitSHA string `json:"agentmesh_commit_sha"`
	Timeout            time.Duration
}

// Report is transport evidence only. It cannot raise SQL Sentinel's evidence
// level or performance-claim eligibility.
type Report struct {
	Status                      string `json:"status"`
	Code                        string `json:"code,omitempty"`
	EvidenceLevel               string `json:"evidence_level"`
	EligibleForPerformanceClaim bool   `json:"eligible_for_performance_claim"`
	AgentMeshCommitSHA          string `json:"agentmesh_commit_sha,omitempty"`
	TraceID                     string `json:"agentmesh_trace_id,omitempty"`
	HTTPStatus                  int    `json:"http_status,omitempty"`
	SSECompleted                bool   `json:"sse_completed"`
}

// LoadConfig validates configuration before any request can be made.
func LoadConfig(lookup func(string) string) (Config, Report) {
	cfg := Config{
		BaseURL:            strings.TrimSpace(lookup("AGENTMESH_BASE_URL")),
		APIKey:             strings.TrimSpace(lookup("AGENTMESH_API_KEY")),
		Model:              strings.TrimSpace(lookup("AGENTMESH_MODEL")),
		AgentMeshCommitSHA: strings.TrimSpace(lookup("AGENTMESH_COMMIT_SHA")),
		Timeout:            90 * time.Second,
	}
	report := newReport(StatusUnavailable, "", cfg)
	if cfg.BaseURL == "" || cfg.APIKey == "" || cfg.Model == "" || cfg.AgentMeshCommitSHA == "" {
		report.Code = ReasonConfigurationMissing
		return Config{}, report
	}
	if err := validateBaseURL(cfg.BaseURL); err != nil {
		report.Code = ReasonConfigurationInvalid
		return Config{}, report
	}
	return cfg, Report{}
}

// Run consumes the protocol stream in memory. Delta text is deliberately
// discarded: a successful protocol exchange is not SQL or model evidence.
func Run(ctx context.Context, client *http.Client, cfg Config) Report {
	report := newReport(StatusFailed, "", cfg)
	if err := validateConfig(cfg); err != nil {
		report.Status = StatusUnavailable
		report.Code = ReasonConfigurationInvalid
		return report
	}
	if client == nil {
		client = http.DefaultClient
	}
	requestCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	body, err := json.Marshal(chatRequest{
		Model:    cfg.Model,
		Messages: []chatMessage{{Role: "user", Content: ConnectivityPrompt}},
		Stream:   true,
	})
	if err != nil {
		report.Code = ReasonRequestFailed
		return report
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		report.Code = ReasonConfigurationInvalid
		return report
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			report.Code = ReasonTimeout
		} else {
			report.Code = ReasonRequestFailed
		}
		return report
	}
	defer resp.Body.Close()
	report.HTTPStatus = resp.StatusCode
	if resp.StatusCode != http.StatusOK {
		report.Code = rejectionCode(resp.StatusCode)
		return report
	}
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		report.Code = ReasonResponseNotSSE
		return report
	}
	report.TraceID = strings.TrimSpace(resp.Header.Get("X-AgentMesh-Trace-ID"))
	if report.TraceID == "" {
		report.Code = ReasonTraceMissing
		return report
	}

	done, streamCode, err := consumeSSE(resp.Body)
	if err != nil {
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			report.Code = ReasonTimeout
		} else {
			report.Code = ReasonStreamInterrupted
		}
		return report
	}
	if streamCode != "" {
		report.Code = ReasonStreamInterrupted
		return report
	}
	if !done {
		report.Code = ReasonStreamIncomplete
		return report
	}
	report.Status = StatusCompleted
	report.Code = "success"
	report.SSECompleted = true
	return report
}

func newReport(status, code string, cfg Config) Report {
	return Report{
		Status:                      status,
		Code:                        code,
		EvidenceLevel:               EvidenceLevel,
		EligibleForPerformanceClaim: false,
		AgentMeshCommitSHA:          cfg.AgentMeshCommitSHA,
	}
}

func validateConfig(cfg Config) error {
	if strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.Model) == "" || strings.TrimSpace(cfg.AgentMeshCommitSHA) == "" || cfg.Timeout <= 0 {
		return errors.New("missing configuration")
	}
	return validateBaseURL(cfg.BaseURL)
}

func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("base URL must be http://127.0.0.1:PORT")
	}
	return nil
}

func rejectionCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return ReasonAuthenticationFailed
	case http.StatusTooManyRequests:
		return ReasonRateLimited
	default:
		return ReasonHTTPRejected
	}
}

func consumeSSE(r io.Reader) (done bool, streamCode string, err error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024), 128*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			return true, "", nil
		}
		var event streamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return false, "", fmt.Errorf("decode SSE event: %w", err)
		}
		if event.Error != nil && event.Error.Code != "" {
			return false, event.Error.Code, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, "", err
	}
	return false, "", nil
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type streamEvent struct {
	Error *struct {
		Code string `json:"code"`
	} `json:"error,omitempty"`
}
