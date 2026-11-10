package agentrun

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"sqlsentinel/internal/identity"
	"sqlsentinel/internal/sqladmit"
)

type Service struct {
	db     *sql.DB
	config Config
	models map[string]Model
	tasks  Tasks
	check  Check
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var runID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var modelAliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,95}$`)

func New(db *sql.DB, cfg Config, models map[string]Model, tasks Tasks, check Check) (*Service, error) {
	if db == nil || check == nil || tasks == nil || cfg.Version == "" || len(cfg.Version) > 64 || !modelAliasPattern.MatchString(cfg.DefaultModel) || len(cfg.ModelAliases) == 0 || len(cfg.ModelAliases) > 8 {
		return nil, ErrInvalid
	}
	copied := map[string]Model{}
	aliases := map[string]bool{}
	for _, alias := range cfg.ModelAliases {
		if !modelAliasPattern.MatchString(alias) || aliases[alias] || models[alias] == nil {
			return nil, ErrInvalid
		}
		copied[alias] = models[alias]
		aliases[alias] = true
	}
	if !aliases[cfg.DefaultModel] {
		return nil, ErrInvalid
	}
	cfg.ModelAliases = append([]string(nil), cfg.ModelAliases...)
	return &Service{db: db, config: cfg, models: copied, tasks: tasks, check: check}, nil
}
func (s *Service) Configuration(p identity.Principal) PublicConfig {
	return PublicConfig{Enabled: true, Version: s.config.Version, ModelAliases: append([]string(nil), s.config.ModelAliases...), DefaultModel: s.config.DefaultModel, ToolsEnabled: s.config.ToolsEnabled, MaxModelCalls: MaxModelCalls, MaxJobs: MaxJobs, RunSeconds: RunSeconds, InnerModelCalls: 5, InnerEvaluations: 2, TenantID: p.Tenant, ProjectID: p.Project, Tables: append([]string(nil), p.Tables...), ModelAvailable: len(s.models) > 0, TaskServiceAvailable: s.tasks != nil}
}
func inputDigest(in Input) string {
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (s *Service) Create(ctx context.Context, p identity.Principal, in Input) (Run, bool, error) {
	if strings.TrimSpace(in.SQL) == "" {
		return Run{}, false, ErrSQLRequired
	}
	if !safeID.MatchString(in.RequestID) || len(in.SQL) > 64<<10 || len(in.Message) > 4096 || !sqladmit.Admit(in.SQL).Accepted {
		return Run{}, false, ErrInvalid
	}
	if p.Role != "writer" && p.Role != "admin" {
		return Run{}, false, ErrForbidden
	}
	if err := s.check(ctx, p, in.SQL); err != nil {
		return Run{}, false, checkError(err)
	}
	// Hash the caller's exact logical input, not a later deployment's defaults.
	digest := inputDigest(in)
	existing, err := s.byRequest(ctx, p, in.RequestID, digest)
	if err == nil {
		return existing, true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Run{}, false, err
	}
	model := in.ModelAlias
	if model == "" {
		model = s.config.DefaultModel
	}
	if s.models[model] == nil {
		return Run{}, false, ErrInvalid
	}
	tools := s.config.ToolsEnabled
	if in.ToolsEnabled != nil {
		tools = *in.ToolsEnabled
	}
	if tools && !s.config.ToolsEnabled {
		return Run{}, false, ErrForbidden
	}
	id, err := newID()
	if err != nil {
		return Run{}, false, err
	}
	now := time.Now().UTC()
	r := Run{ID: id, RequestID: in.RequestID, SQL: in.SQL, Message: in.Message, ModelAlias: model, ToolsEnabled: tools, ConfigVersion: s.config.Version, Status: "queued", Events: []Event{}, CreatedAt: now, UpdatedAt: now, Deadline: now.Add(RunSeconds * time.Second), principal: p, session: p.SessionID, phase: "decide"}
	addEvent(&r, "queued", "请求已保存，等待模型决策。")
	body, _ := json.Marshal(r)
	principal, _ := json.Marshal(p)
	_, err = s.db.ExecContext(ctx, `INSERT INTO sql_sentinel_agent_runs(run_id,tenant_id,project_id,actor,request_id,input_hash,principal,session_id,document,status,phase,deadline,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,'queued','decide',?,?,?)`, id, p.Tenant, p.Project, p.Subject, in.RequestID, digest, principal, p.SessionID, body, r.Deadline, now, now)
	if err != nil {
		var duplicate *mysql.MySQLError
		if errors.As(err, &duplicate) && duplicate.Number == 1062 {
			old, e := s.byRequest(ctx, p, in.RequestID, digest)
			return old, true, e
		}
		return Run{}, false, ErrUnavailable
	}
	return r, false, nil
}
func (s *Service) byRequest(ctx context.Context, p identity.Principal, request, digest string) (Run, error) {
	var id, actual string
	err := s.db.QueryRowContext(ctx, `SELECT run_id,input_hash FROM sql_sentinel_agent_runs WHERE tenant_id=? AND project_id=? AND actor=? AND request_id=?`, p.Tenant, p.Project, p.Subject, request).Scan(&id, &actual)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, ErrUnavailable
	}
	if actual != digest {
		return Run{}, ErrConflict
	}
	return s.Get(ctx, p, id)
}
func (s *Service) Get(ctx context.Context, p identity.Principal, id string) (Run, error) {
	if !runID.MatchString(id) {
		return Run{}, ErrNotFound
	}
	r, err := s.load(ctx, id)
	if err != nil {
		return Run{}, err
	}
	if !owns(p, r) {
		return Run{}, ErrNotFound
	}
	if err = s.check(ctx, p, r.SQL); err != nil {
		return Run{}, checkError(err)
	}
	return r, nil
}

// GetByRequest recovers a response-lost create without performing a new write.
// The lookup is scoped to the member; Get checks the member's current access.
func (s *Service) GetByRequest(ctx context.Context, p identity.Principal, request string) (Run, error) {
	if !safeID.MatchString(request) {
		return Run{}, ErrNotFound
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT run_id FROM sql_sentinel_agent_runs WHERE tenant_id=? AND project_id=? AND actor=? AND request_id=?`, p.Tenant, p.Project, p.Subject, request).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, ErrUnavailable
	}
	return s.Get(ctx, p, id)
}
func (s *Service) List(ctx context.Context, p identity.Principal) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id FROM sql_sentinel_agent_runs WHERE tenant_id=? AND project_id=? AND actor=? ORDER BY created_at DESC LIMIT 30`, p.Tenant, p.Project, p.Subject)
	if err != nil {
		return nil, ErrUnavailable
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []Run{}
	for _, id := range ids {
		r, e := s.Get(ctx, p, id)
		if errors.Is(e, ErrForbidden) {
			continue
		}
		if e != nil {
			return nil, e
		}
		result = append(result, r)
	}
	return result, nil
}
func (s *Service) Cancel(ctx context.Context, p identity.Principal, id string) (Run, error) {
	r, err := s.Get(ctx, p, id)
	if err != nil {
		return r, err
	}
	if p.Role != "writer" && p.Role != "admin" {
		return Run{}, ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	r, err = scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM sql_sentinel_agent_runs WHERE run_id=? FOR UPDATE", id))
	if err != nil {
		return r, err
	}
	if terminal(r.Status) {
		return r, nil
	}
	r.Status = "cancelled"
	r.StopReason = "cancel_requested"
	r.Answer = "运行已取消；后台正在确认其诊断任务的取消结果。"
	r.pendingCancel = true
	r.UpdatedAt = time.Now().UTC()
	addEvent(&r, "cancel_requested", "已持久保存取消请求，不会再派发新的模型决策。")
	doc, _ := json.Marshal(r)
	_, err = tx.ExecContext(ctx, `UPDATE sql_sentinel_agent_runs SET document=?,status='cancelled',pending_cancel=true,updated_at=UTC_TIMESTAMP(6) WHERE run_id=?`, doc, id)
	if err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	return r, nil
}
func addEvent(r *Run, kind, message string) {
	if len(message) > 512 {
		message = message[:512]
	}
	r.Events = append(r.Events, Event{Kind: kind, Message: message, At: time.Now().UTC()})
	if len(r.Events) > MaxEvents {
		r.Events = append([]Event(nil), r.Events[len(r.Events)-MaxEvents:]...)
	}
}
