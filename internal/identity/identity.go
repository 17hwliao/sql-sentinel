// Package identity binds short-lived JWT sessions to server-side scope and roles.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/golang-jwt/jwt/v5"
	"regexp"
	"strings"
	"time"
)

type Principal struct {
	Subject string   `json:"subject"`
	Role    string   `json:"role"`
	Tenant  string   `json:"tenant_id"`
	Project string   `json:"project_id"`
	Tables  []string `json:"tables,omitempty"`
}
type claims struct {
	Principal
	jwt.RegisteredClaims
}
type contextKey struct{}

func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}
func From(ctx context.Context) Principal { p, _ := ctx.Value(contextKey{}).(Principal); return p }

type Auth struct {
	DB        *sql.DB
	Key       []byte
	Bootstrap string
	Policy    *casbin.SyncedEnforcer
}

var name = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func New(db *sql.DB, key []byte, bootstrap string) (*Auth, error) {
	if len(key) < 32 || len(bootstrap) < 32 {
		return nil, errors.New("identity_secrets_required")
	}
	m, err := model.NewModelFromString(`[request_definition]
r = role, action
[policy_definition]
p = role, action
[policy_effect]
e = some(where (p.eft == allow))
[matchers]
m = r.role == p.role && (p.action == "*" || r.action == p.action)`)
	if err != nil {
		return nil, err
	}
	e, err := casbin.NewSyncedEnforcer(m)
	if err != nil {
		return nil, err
	}
	for role, actions := range map[string][]string{"admin": {"*"}, "reader": {"job.read", "rag.query"}, "writer": {"job.read", "job.create", "rag.query", "rag.write"}, "reviewer": {"job.read", "approval.decide", "benchmark.create", "audit.read", "rag.query"}} {
		for _, action := range actions {
			if _, err = e.AddPolicy(role, action); err != nil {
				return nil, err
			}
		}
	}
	return &Auth{DB: db, Key: key, Bootstrap: bootstrap, Policy: e}, nil
}
func (a *Auth) Authenticate(ctx context.Context, header string) (Principal, error) {
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || len(token) > 4096 {
		return Principal{}, errors.New("unauthorized")
	}
	expected := sha256.Sum256([]byte(a.Bootstrap))
	actual := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(expected[:], actual[:]) == 1 {
		return Principal{Subject: "operator", Role: "admin", Tenant: "operator", Project: "default", Tables: []string{"*"}}, nil
	}
	c := &claims{}
	parsed, err := jwt.ParseWithClaims(token, c, func(*jwt.Token) (any, error) { return a.Key, nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("sql-sentinel"), jwt.WithAudience("sql-sentinel"), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil || !parsed.Valid || c.IssuedAt == nil || c.ExpiresAt.Sub(c.IssuedAt.Time) > time.Hour || c.ID == "" || !valid(c.Principal) {
		return Principal{}, errors.New("unauthorized")
	}
	var count int
	err = a.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sql_sentinel_sessions WHERE token_id=? AND subject_id=? AND tenant_id=? AND project_id=? AND role_name=? AND revoked_at IS NULL AND expires_at>UTC_TIMESTAMP(6)`, c.ID, c.Principal.Subject, c.Tenant, c.Project, c.Role).Scan(&count)
	if err != nil {
		return Principal{}, errors.New("identity_unavailable")
	}
	if count != 1 {
		return Principal{}, errors.New("unauthorized")
	}
	return c.Principal, nil
}
func valid(p Principal) bool {
	if len(p.Tables) > 16 {
		return false
	}
	for _, table := range p.Tables {
		if !name.MatchString(table) {
			return false
		}
	}
	return name.MatchString(p.Subject) && name.MatchString(p.Tenant) && name.MatchString(p.Project) && (p.Role == "admin" || p.Role == "writer" || p.Role == "reader" || p.Role == "reviewer")
}
func (a *Auth) Allowed(p Principal, action string) bool {
	allowed, err := a.Policy.Enforce(p.Role, action)
	return err == nil && allowed
}
func (a *Auth) Issue(ctx context.Context, p Principal) (string, string, error) {
	if !valid(p) {
		return "", "", errors.New("invalid_principal")
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	id := hex.EncodeToString(raw)
	now := time.Now().UTC()
	until := now.Add(time.Hour)
	c := claims{Principal: p, RegisteredClaims: jwt.RegisteredClaims{Issuer: "sql-sentinel", Audience: jwt.ClaimStrings{"sql-sentinel"}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(until), ID: id}}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(a.Key)
	if err != nil {
		return "", "", err
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT INTO sql_sentinel_sessions(token_id,subject_id,tenant_id,project_id,role_name,expires_at) VALUES (?,?,?,?,?,?)", id, p.Subject, p.Tenant, p.Project, p.Role, until)
	if err == nil {
		issuer := From(ctx)
		_, err = tx.ExecContext(ctx, "INSERT INTO sql_sentinel_audit(tenant_id,project_id,actor,action,resource_id,metadata) VALUES (?,?,?,'token.issued',?,JSON_OBJECT('role',?,'subject',?))", p.Tenant, p.Project, issuer.Subject, id, p.Role, p.Subject)
	}
	if err == nil {
		err = tx.Commit()
	}
	return token, id, err
}
