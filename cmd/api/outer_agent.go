package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"regexp"
	"strings"

	"sqlsentinel/internal/agentgraph"
	"sqlsentinel/internal/agentrun"
	"sqlsentinel/internal/service"
)

var outerAlias = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,95}$`)

// Closed by default. Endpoints and credentials are deployment-only settings,
// never browser-supplied URLs, database handles or elevated service identities.
func configureOuterAgent(ctx context.Context, s *service.Server) error {
	enabled := os.Getenv("SQL_SENTINEL_OUTER_AGENT_ENABLED")
	if enabled == "" || enabled == "false" {
		return nil
	}
	if enabled != "true" {
		return errors.New("outer_agent_enabled_invalid")
	}
	endpoint, err := url.Parse(os.Getenv("EINO_BASE_URL"))
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") {
		return errors.New("outer_agent_gateway_url_required")
	}
	aliases := strings.Split(os.Getenv("SQL_SENTINEL_OUTER_MODELS"), ",")
	if len(aliases) == 1 && strings.TrimSpace(aliases[0]) == "" {
		aliases = []string{os.Getenv("EINO_MODEL")}
	}
	if len(aliases) > 8 {
		return errors.New("outer_agent_models_invalid")
	}
	models := map[string]agentrun.Model{}
	for i, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if !outerAlias.MatchString(alias) || models[alias] != nil {
			return errors.New("outer_agent_models_invalid")
		}
		aliases[i] = alias
		node, err := agentgraph.NewBoundedOpenAINode(ctx, os.Getenv("OPENAI_API_KEY"), alias, os.Getenv("EINO_BASE_URL"), 768)
		if err != nil {
			return errors.New("outer_agent_model_configuration_invalid")
		}
		model, ok := node.(agentrun.Model)
		if !ok {
			return errors.New("outer_agent_model_adapter_invalid")
		}
		models[alias] = model
	}
	version := os.Getenv("SQL_SENTINEL_OUTER_CONFIG_VERSION")
	if version == "" {
		version = "outer.v1"
	}
	if !outerAlias.MatchString(version) {
		return errors.New("outer_agent_version_invalid")
	}
	tools := os.Getenv("SQL_SENTINEL_OUTER_TOOLS_ENABLED")
	if tools != "" && tools != "true" && tools != "false" {
		return errors.New("outer_agent_tools_invalid")
	}
	runs, err := agentrun.New(s.Store.DB, agentrun.Config{Version: version, DefaultModel: aliases[0], ModelAliases: aliases, ToolsEnabled: tools != "false"}, models, service.RunTasks{Server: s}, s.CheckAgentRunPrincipal)
	if err != nil {
		return errors.New("outer_agent_configuration_invalid")
	}
	if err = runs.CheckSchema(ctx); err != nil {
		return errors.New("outer_agent_schema_unavailable")
	}
	s.AgentRuns = runs
	go runs.Run(ctx)
	return nil
}
