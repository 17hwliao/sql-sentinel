package main

import "testing"

func TestProposalAPIKeyPrefersOpenAIAndFallsBackToAnthropicRouterToken(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "router-token")
	if got := proposalAPIKey(); got != "openai-key" {
		t.Fatalf("proposalAPIKey() = %q, want OpenAI key", got)
	}

	t.Setenv("OPENAI_API_KEY", "")
	if got := proposalAPIKey(); got != "router-token" {
		t.Fatalf("proposalAPIKey() fallback = %q, want router token", got)
	}
}
