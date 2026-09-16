package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"sqlsentinel/internal/agentgraph"
	"sqlsentinel/internal/agentmeshbridge"
)

const reasonRuntimeModelConfigurationMissing = "runtime_model_configuration_missing"

func runProposeCandidate(args []string) error {
	fs := flag.NewFlagSet("propose-candidate", flag.ExitOnError)
	provider := fs.String("provider", "", "model provider (openai or agentmesh)")
	evidenceDir := fs.String("evidence-dir", "", "directory containing four pipeline evidence JSON files")
	out := fs.String("out", "", "candidate proposal report JSON path")
	maxAttempts := fs.Int("max-attempts", 2, "maximum invalid CandidateSpec retries (1-3)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*provider != "openai" && *provider != "agentmesh") || *evidenceDir == "" || *out == "" {
		return fmt.Errorf("--provider openai|agentmesh, --evidence-dir and --out are required")
	}
	if *maxAttempts < 1 || *maxAttempts > 3 {
		return fmt.Errorf("--max-attempts must be between 1 and 3")
	}
	evidence, err := agentgraph.LoadEvidence(*evidenceDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var modelNode agentgraph.DraftNode
	modelName := ""
	if *provider == "openai" {
		apiKey := proposalAPIKey()
		modelName = os.Getenv("EINO_MODEL")
		if apiKey == "" || modelName == "" {
			return fmt.Errorf("%s: OPENAI_API_KEY (or ANTHROPIC_AUTH_TOKEN) and EINO_MODEL are required for --provider openai", reasonRuntimeModelConfigurationMissing)
		}
		modelNode, err = agentgraph.NewOpenAINode(ctx, apiKey, modelName, os.Getenv("EINO_BASE_URL"))
		if err != nil {
			return fmt.Errorf("create OpenAI model: %w", err)
		}
	} else {
		cfg, report := agentmeshbridge.LoadConfig(os.Getenv)
		if report.Code != "" {
			return fmt.Errorf("agentmesh candidate configuration: %s", report.Code)
		}
		modelName = cfg.Model
		modelNode = agentMeshDraftNode{config: cfg}
	}
	graph, err := agentgraph.NewEinoGraph(modelNode)
	if err != nil {
		return fmt.Errorf("compile Eino graph: %w", err)
	}
	report, err := agentgraph.Propose(ctx, evidence, graph, *maxAttempts)
	if err != nil {
		return err
	}
	report.Model = modelName
	f, err := os.Create(*out)
	if err != nil {
		return fmt.Errorf("create candidate proposal report: %w", err)
	}
	if err := agentgraph.WriteJSON(f, report); err != nil {
		f.Close()
		return fmt.Errorf("write candidate proposal report: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("candidate proposal: %s (L1, performance claim eligible=false, attempts=%d, accepted=%t)\n", *out, report.Attempts, report.CandidateSpec != nil)
	return nil
}

type agentMeshDraftNode struct{ config agentmeshbridge.Config }

func (n agentMeshDraftNode) Draft(ctx context.Context, prompt string) (string, error) {
	return agentmeshbridge.DraftCandidate(ctx, nil, n.config, prompt)
}

func proposalAPIKey() string {
	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		return apiKey
	}
	return os.Getenv("ANTHROPIC_AUTH_TOKEN")
}
