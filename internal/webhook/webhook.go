// Package webhook implements a local-only, signature-gated PR review entry
// point. It treats every delivery field and diff line as untrusted text.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"sqlsentinel/internal/kafka"
	"sqlsentinel/internal/pipeline"
	"sqlsentinel/internal/sqladmit"
)

const (
	Path                  = "/webhook/pr"
	SignatureHeader       = "X-SQL-Sentinel-Signature"
	MaxBodyBytes    int64 = 1 << 20

	ReasonMethodNotAllowed = "method_not_allowed"
	ReasonSignatureMissing = "signature_missing"
	ReasonInvalidSignature = "invalid_signature"
	ReasonBodyTooLarge     = "body_too_large"
	ReasonQueueFull        = "queue_full"
	ReasonInvalidEvent     = "invalid_event"
	ReasonInvalidDelivery  = "invalid_delivery_id"
	ReasonNoSQL            = "no_sql_candidates"
	ReasonMultipleSQL      = "multiple_sql_candidates"
	ReasonSQLRejected      = "sql_admission_rejected"
	ReasonPipelineFailed   = "pipeline_unavailable"
	ReasonDeliveryExists   = "delivery_output_exists"
	ReasonDeliveryWrite    = "delivery_output_write_failed"
)

var deliveryID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

var (
	errDeliveryOutputExists = errors.New(ReasonDeliveryExists)
	errDeliveryOutputWrite  = errors.New(ReasonDeliveryWrite)
)

type Event struct {
	DeliveryID string `json:"delivery_id"`
	PRNumber   int    `json:"pr_number"`
	Diff       string `json:"diff"`
}

type Reason struct {
	Code string `json:"code"`
}

type DeliveryReport struct {
	DeliveryID                  string              `json:"delivery_id"`
	PRNumber                    int                 `json:"pr_number"`
	JobID                       string              `json:"job_id,omitempty"`
	Duplicate                   bool                `json:"duplicate,omitempty"`
	VerificationStatus          string              `json:"verification_status"`
	EvidenceLevel               string              `json:"evidence_level"`
	EligibleForPerformanceClaim bool                `json:"eligible_for_performance_claim"`
	Admissions                  []sqladmit.Result   `json:"sql_admissions"`
	CompletedSteps              []string            `json:"completed_steps"`
	RejectionReasons            []Reason            `json:"rejection_reasons"`
	Artifacts                   []pipeline.Artifact `json:"artifacts"`
}

// PipelineRunner is injected so admission, output rendering and concurrency
// behavior can be tested without a shadow database.
type PipelineRunner func(context.Context, []byte, string) (pipeline.Report, error)

type Config struct {
	Secret        []byte
	OutputRoot    string
	MaxConcurrent int
	RunPipeline   PipelineRunner
	// AsyncEnqueue switches the verified single-SQL path to the durable Kafka
	// workflow. It must be paired with CandidateSpec; the spec is stored in the
	// control plane and never sent in the Kafka event.
	CandidateSpec []byte
	AsyncEnqueue  func(context.Context, kafka.ReceiveInput) (kafka.ReceiveResult, error)
}

type Server struct {
	secret        []byte
	outputRoot    string
	runPipeline   PipelineRunner
	candidateSpec []byte
	asyncEnqueue  func(context.Context, kafka.ReceiveInput) (kafka.ReceiveResult, error)
	sem           chan struct{}
}

func New(config Config) (*Server, error) {
	if len(config.Secret) == 0 {
		return nil, errors.New("webhook secret is required")
	}
	if config.OutputRoot == "" {
		return nil, errors.New("webhook output root is required")
	}
	if config.MaxConcurrent < 1 || config.MaxConcurrent > 4 {
		return nil, errors.New("max concurrent must be between 1 and 4")
	}
	if config.RunPipeline == nil && config.AsyncEnqueue == nil {
		return nil, errors.New("pipeline runner or async enqueue is required")
	}
	if config.AsyncEnqueue != nil && len(config.CandidateSpec) == 0 {
		return nil, errors.New("CandidateSpec is required for async enqueue")
	}
	if err := os.MkdirAll(config.OutputRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create webhook output root: %w", err)
	}
	return &Server{
		secret:        append([]byte(nil), config.Secret...),
		outputRoot:    config.OutputRoot,
		runPipeline:   config.RunPipeline,
		candidateSpec: append([]byte(nil), config.CandidateSpec...),
		asyncEnqueue:  config.AsyncEnqueue,
		sem:           make(chan struct{}, config.MaxConcurrent),
	}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeRejection(w, http.StatusMethodNotAllowed, ReasonMethodNotAllowed)
		return
	}
	provided, ok := parseSignature(r.Header.Get(SignatureHeader))
	if !ok {
		writeRejection(w, http.StatusUnauthorized, ReasonSignatureMissing)
		return
	}
	body, tooLarge, err := readBody(r.Body)
	if err != nil {
		writeRejection(w, http.StatusBadRequest, ReasonInvalidEvent)
		return
	}
	if tooLarge {
		writeRejection(w, http.StatusRequestEntityTooLarge, ReasonBodyTooLarge)
		return
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), provided) {
		writeRejection(w, http.StatusUnauthorized, ReasonInvalidSignature)
		return
	}

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		writeRejection(w, http.StatusTooManyRequests, ReasonQueueFull)
		return
	}

	event, err := decodeEvent(body)
	if err != nil {
		writeRejection(w, http.StatusBadRequest, ReasonInvalidEvent)
		return
	}
	if !deliveryID.MatchString(event.DeliveryID) || event.PRNumber < 1 {
		writeRejection(w, http.StatusBadRequest, ReasonInvalidDelivery)
		return
	}
	report, err := s.process(r.Context(), event)
	if err != nil {
		log.Printf("webhook delivery %q failed: %v", event.DeliveryID, err)
		writeRejection(w, http.StatusConflict, deliveryErrorCode(err))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"delivery_id": event.DeliveryID, "verification_status": report.VerificationStatus,
		"job_id": report.JobID, "duplicate": report.Duplicate,
	})
}

func (s *Server) process(ctx context.Context, event Event) (DeliveryReport, error) {
	sqls := ExtractAddedSQL(event.Diff)
	report := DeliveryReport{
		DeliveryID: event.DeliveryID, PRNumber: event.PRNumber,
		VerificationStatus: "verification_unavailable", EvidenceLevel: "",
		EligibleForPerformanceClaim: false, Admissions: []sqladmit.Result{},
		CompletedSteps: []string{}, RejectionReasons: []Reason{}, Artifacts: []pipeline.Artifact{},
	}
	for _, sql := range sqls {
		report.Admissions = append(report.Admissions, sqladmit.Admit(sql))
	}
	if len(sqls) == 0 {
		report.RejectionReasons = append(report.RejectionReasons, Reason{Code: ReasonNoSQL})
		return s.persist(report)
	}
	for _, admission := range report.Admissions {
		if !admission.Accepted {
			report.RejectionReasons = append(report.RejectionReasons, Reason{Code: ReasonSQLRejected}, Reason{Code: admission.ReasonCode})
		}
	}
	if len(report.RejectionReasons) != 0 {
		return s.persist(report)
	}
	if len(sqls) != 1 {
		report.RejectionReasons = append(report.RejectionReasons, Reason{Code: ReasonMultipleSQL})
		return s.persist(report)
	}
	if s.asyncEnqueue != nil {
		result, err := s.asyncEnqueue(ctx, kafka.ReceiveInput{
			DeliveryID: event.DeliveryID, PRNumber: event.PRNumber,
			SQL: []byte(sqls[0]), CandidateSpec: append([]byte(nil), s.candidateSpec...),
		})
		if err != nil {
			return DeliveryReport{}, fmt.Errorf("enqueue delivery: %w", err)
		}
		report.JobID = result.Job.ID
		report.Duplicate = result.Duplicate
		report.VerificationStatus = "queued"
		return report, nil
	}

	deliveryDir, err := s.createDeliveryDir(event.DeliveryID)
	if err != nil {
		return DeliveryReport{}, err
	}
	pipelineReport, runErr := s.runPipeline(ctx, []byte(sqls[0]), filepath.Join(deliveryDir, "artifacts"))
	report.EvidenceLevel = pipelineReport.EvidenceLevel
	report.EligibleForPerformanceClaim = pipelineReport.EligibleForPerformanceClaim
	report.CompletedSteps = append(report.CompletedSteps, pipelineReport.CompletedSteps...)
	report.Artifacts = append(report.Artifacts, pipelineReport.Artifacts...)
	for _, reason := range pipelineReport.RejectionReasons {
		report.RejectionReasons = append(report.RejectionReasons, Reason{Code: reason.Code})
	}
	if runErr != nil {
		report.RejectionReasons = append(report.RejectionReasons, Reason{Code: ReasonPipelineFailed})
	}
	if runErr == nil && pipelineReport.StoppedStep == "" {
		report.VerificationStatus = "verification_completed"
	}
	return report, s.writeDelivery(deliveryDir, report)
}

func (s *Server) persist(report DeliveryReport) (DeliveryReport, error) {
	deliveryDir, err := s.createDeliveryDir(report.DeliveryID)
	if err != nil {
		return DeliveryReport{}, err
	}
	return report, s.writeDelivery(deliveryDir, report)
}

func (s *Server) createDeliveryDir(id string) (string, error) {
	dir := filepath.Join(s.outputRoot, id)
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", errDeliveryOutputExists
		}
		return "", fmt.Errorf("%w: %v", errDeliveryOutputWrite, err)
	}
	return dir, nil
}

func (s *Server) writeDelivery(dir string, report DeliveryReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "webhook_report.json"), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("%w: %v", errDeliveryOutputWrite, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comment.md"), []byte(renderComment(report)), 0o644); err != nil {
		return fmt.Errorf("%w: %v", errDeliveryOutputWrite, err)
	}
	return nil
}

func ExtractAddedSQL(diff string) []string {
	var sqls []string
	var current strings.Builder
	inSQLFile := false
	flush := func() {
		if sql := strings.TrimSpace(current.String()); sql != "" {
			sqls = append(sqls, sql)
		}
		current.Reset()
	}
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			parts := strings.Fields(line)
			inSQLFile = len(parts) == 4 && strings.HasSuffix(strings.ToLower(parts[3]), ".sql")
			continue
		}
		if inSQLFile && strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			current.WriteString(strings.TrimPrefix(line, "+"))
			current.WriteByte('\n')
		}
	}
	flush()
	return sqls
}

func renderComment(report DeliveryReport) string {
	var b strings.Builder
	evidenceLevel := report.EvidenceLevel
	if evidenceLevel == "" {
		evidenceLevel = "none"
	}
	b.WriteString("# SQL Sentinel verification\n\n")
	fmt.Fprintf(&b, "status: `%s`\n\n", report.VerificationStatus)
	fmt.Fprintf(&b, "evidence_level: `%s`\n\n", evidenceLevel)
	fmt.Fprintf(&b, "eligible_for_performance_claim: `%t`\n\n", report.EligibleForPerformanceClaim)
	if len(report.CompletedSteps) != 0 {
		b.WriteString("completed_steps:\n")
		for _, step := range report.CompletedSteps {
			fmt.Fprintf(&b, "- `%s`\n", step)
		}
	}
	if len(report.RejectionReasons) != 0 {
		b.WriteString("rejection_reasons:\n")
		for _, reason := range report.RejectionReasons {
			fmt.Fprintf(&b, "- `%s`\n", reason.Code)
		}
	}
	if len(report.Artifacts) != 0 {
		b.WriteString("\nartifacts:\n")
		for _, artifact := range report.Artifacts {
			fmt.Fprintf(&b, "- `%s`: `%s`\n", artifact.File, artifact.SHA256)
		}
	}
	return b.String()
}

func deliveryErrorCode(err error) string {
	if errors.Is(err, errDeliveryOutputExists) {
		return ReasonDeliveryExists
	}
	return ReasonDeliveryWrite
}

func parseSignature(header string) ([]byte, bool) {
	if !strings.HasPrefix(header, "sha256=") {
		return nil, false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil || len(provided) != sha256.Size {
		return nil, false
	}
	return provided, true
}

func readBody(body io.ReadCloser) ([]byte, bool, error) {
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, MaxBodyBytes+1))
	if err != nil {
		return nil, false, err
	}
	return data, int64(len(data)) > MaxBodyBytes, nil
}

func decodeEvent(body []byte) (Event, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var event Event
	if err := dec.Decode(&event); err != nil {
		return Event{}, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return Event{}, errors.New("trailing JSON value")
	}
	return event, nil
}

func writeRejection(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"rejection_code": code})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
