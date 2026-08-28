package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"sqlsentinel/internal/evaluation"
)

func runEvaluate(args []string) error {
	fs := flag.NewFlagSet("evaluate", flag.ExitOnError)
	manifest := fs.String("manifest", "examples/evaluation/manifest.json", "strict evaluation manifest")
	validation := fs.String("validation", "validation_result.json", "historical measurement JSON")
	pipelineReport := fs.String("pipeline-report", "", "current smoke pipeline_report.json with step timings")
	out := fs.String("out", "evaluation_report.json", "evaluation report JSON path")
	runSecurity := fs.Bool("run-security-tests", true, "run fixed local adversarial test checklist")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *manifest == "" || *validation == "" || *out == "" {
		return errors.New("--manifest, --validation and --out are required")
	}
	report, err := evaluation.Build(evaluation.Input{ManifestPath: *manifest, ValidationPath: *validation})
	if err != nil {
		return err
	}
	if *pipelineReport != "" {
		if err := evaluation.AttachPipelineTiming(&report, *pipelineReport); err != nil {
			return err
		}
	}
	if *runSecurity {
		report.Security = runEvaluationSecurityTests()
	}
	evaluation.SetEnvironment(&report, gitCommit(), time.Now().UTC().Format(time.RFC3339), gitWorkingTreeDirty())
	f, err := os.Create(*out)
	if err != nil {
		return fmt.Errorf("create evaluation report: %w", err)
	}
	if err := evaluation.WriteJSON(f, report); err != nil {
		f.Close()
		return fmt.Errorf("write evaluation report: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("evaluation report: %s (static=%d/%d, security=%s, eligible_for_performance_claim=%t)\n", *out, report.StaticRules.Detected, report.StaticRules.Total, report.Security.Status, report.EligibleForPerformanceClaim)
	return nil
}

func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(out))
}

func gitWorkingTreeDirty() bool {
	out, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(out)) != ""
}

func runEvaluationSecurityTests() evaluation.Security {
	if _, err := exec.LookPath("go"); err != nil {
		return evaluation.DefaultSecurityChecklist()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return evaluation.RunSecurityChecks(func(pkg string) error {
		return exec.CommandContext(ctx, "go", "test", "-count=1", pkg).Run()
	})
}
