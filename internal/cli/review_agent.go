package cli

import (
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/agentcfg"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
	"github.com/spf13/cobra"
)

// bindReviewerFlags exposes the deliberately narrow per-run override: it
// changes only the fresh review/rereview harness, not Test, Document, Lint, or
// the review fixer. The daemon persists the resulting selection on the run.
func bindReviewerFlags(cmd *cobra.Command, reviewer, model, effort *string) {
	cmd.Flags().StringVar(reviewer, "reviewer", "", "review harness for this run only (for example pi)")
	cmd.Flags().StringVar(model, "reviewer-model", "", "model for the per-run reviewer (requires --reviewer)")
	cmd.Flags().StringVar(effort, "reviewer-effort", "", "reasoning effort for the per-run reviewer")
}

func reviewerFromFlags(cmd *cobra.Command, reviewer, model, effort string) (*config.ReviewAgent, error) {
	reviewerChanged := cmd.Flags().Changed("reviewer")
	modelChanged := cmd.Flags().Changed("reviewer-model")
	effortChanged := cmd.Flags().Changed("reviewer-effort")
	changed := reviewerChanged || modelChanged || effortChanged
	if !changed {
		return nil, nil
	}
	name := types.AgentName(strings.TrimSpace(reviewer))
	if reviewerChanged && name == "" {
		return nil, fmt.Errorf("--reviewer must not be empty")
	}
	if !reviewerChanged {
		return nil, fmt.Errorf("--reviewer is required with --reviewer-model or --reviewer-effort")
	}
	if modelChanged && strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("--reviewer-model must not be empty")
	}
	if effortChanged && strings.TrimSpace(effort) == "" {
		return nil, fmt.Errorf("--reviewer-effort must not be empty")
	}
	parsedEffort, err := agentcfg.ParseEffort(effort)
	if err != nil {
		return nil, fmt.Errorf("--reviewer-effort: %w", err)
	}
	entry := config.ReviewAgent{Agent: name, Model: strings.TrimSpace(model), Effort: parsedEffort}
	normalized, err := config.NormalizeReviewAgent(entry)
	if err != nil {
		return nil, err
	}
	return &normalized, nil
}

func resolveRunReviewer(client *ipc.Client, reviewer *config.ReviewAgent) (*config.ReviewAgent, error) {
	if reviewer == nil {
		return nil, nil
	}
	var resolved config.ReviewAgent
	if err := client.Call(ipc.MethodResolveReviewAgent, reviewer, &resolved); err != nil {
		return nil, err
	}
	normalized, err := config.NormalizeReviewAgent(resolved)
	if err != nil {
		return nil, fmt.Errorf("invalid reviewer response: %w", err)
	}
	return &normalized, nil
}
