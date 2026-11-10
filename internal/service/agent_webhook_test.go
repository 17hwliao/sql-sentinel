package service

import "testing"

func TestAgentWebhookOptInBindsScopeAndPreservesDefault(t *testing.T) {
	legacy, err := (&GitHub{}).jobOptions("SELECT id FROM orders")
	if err != nil || legacy.AgentMode != "" || legacy.AgentDecisions != 0 {
		t.Fatal("legacy webhook semantics changed")
	}
	g := &GitHub{AgentMode: true, AllowedTables: map[string]bool{"orders": true}}
	options, err := g.jobOptions("SELECT id FROM orders")
	if err != nil || options.AgentVersion != "agent.v1" || options.AgentDecisions != 5 || options.AgentEvaluations != 2 || options.AgentTables != "orders" || options.Actor != "operator" {
		t.Fatal("unbound webhook agent options")
	}
	if _, err = g.jobOptions("SELECT id FROM secrets"); err == nil {
		t.Fatal("scope expanded")
	}
	g.AllowedTables = nil
	if _, err = g.jobOptions("SELECT id FROM orders"); err == nil {
		t.Fatal("missing operator scope accepted")
	}
}
