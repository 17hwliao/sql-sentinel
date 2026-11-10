package main

import (
	"context"
	"sqlsentinel/internal/service"
	"testing"
)

func TestOuterAgentDefaultsClosedAndRequiresGateway(t *testing.T) {
	t.Setenv("SQL_SENTINEL_OUTER_AGENT_ENABLED", "")
	backend := &service.Server{}
	if err := configureOuterAgent(context.Background(), backend); err != nil || backend.AgentRuns != nil {
		t.Fatal("default enabled outer model traffic")
	}
	t.Setenv("SQL_SENTINEL_OUTER_AGENT_ENABLED", "true")
	t.Setenv("EINO_BASE_URL", "")
	if err := configureOuterAgent(context.Background(), backend); err == nil {
		t.Fatal("missing gateway could silently fall back to another provider")
	}
	for _, endpoint := range []string{"https://key:secret@api.invalid/v1", "https://api.invalid/v1?key=secret", "file:///private/key"} {
		t.Setenv("EINO_BASE_URL", endpoint)
		if err := configureOuterAgent(context.Background(), backend); err == nil {
			t.Fatal("unsafe gateway URL accepted")
		}
	}
}
