package service

import (
	"errors"
	workflow "sqlsentinel/internal/kafka"
	"sqlsentinel/internal/sqladmit"
	"strings"
)

// A signed, allowlisted PR is an operator-owned task, as in the existing fixed
// path. Opting in binds the version/budgets at creation, not at worker deploy.
func (g *GitHub) jobOptions(query string) (workflow.JobOptions, error) {
	if !g.AgentMode {
		return workflow.JobOptions{}, nil
	}
	tables, err := sqladmit.Inspect(query)
	if err != nil || len(tables) == 0 || g.AllowedTables == nil {
		return workflow.JobOptions{}, errors.New("agent_scope_missing")
	}
	for _, table := range tables {
		if !g.AllowedTables[table] {
			return workflow.JobOptions{}, errors.New("agent_scope_rejected")
		}
	}
	return workflow.JobOptions{TenantID: "operator", ProjectID: "default", Actor: "operator", AgentMode: "agent", AgentVersion: "agent.v1", AgentDecisions: 5, AgentEvaluations: 2, AgentTables: strings.Join(tables, ","), AuthorizationRole: "admin"}, nil
}
