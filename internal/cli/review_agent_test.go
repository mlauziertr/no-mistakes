package cli

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/no-mistakes/internal/agentcfg"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestReviewerFromFlagsDefaultsToPiForGrokModel(t *testing.T) {
	cmd := &cobra.Command{}
	var reviewer, model, effort string
	bindReviewerFlags(cmd, &reviewer, &model, &effort)
	if err := cmd.ParseFlags([]string{"--reviewer-model", "xai/grok-4.6", "--reviewer-effort", "high"}); err != nil {
		t.Fatal(err)
	}
	got, err := reviewerFromFlags(cmd, reviewer, model, effort)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Agent != types.AgentPi || got.Model != "xai/grok-4.6" || got.Effort != agentcfg.EffortHigh {
		t.Fatalf("reviewer = %#v", got)
	}
}

func TestReviewerFromFlagsRejectsCredentialShapedModel(t *testing.T) {
	cmd := &cobra.Command{}
	var reviewer, model, effort string
	bindReviewerFlags(cmd, &reviewer, &model, &effort)
	if err := cmd.ParseFlags([]string{"--reviewer-model", "https://secret@example.test/model"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reviewerFromFlags(cmd, reviewer, model, effort); err == nil {
		t.Fatal("credential-shaped reviewer model was accepted")
	}
}

func TestReviewerFromFlagsRejectsEmptyExplicitValues(t *testing.T) {
	for _, args := range [][]string{{"--reviewer", ""}, {"--reviewer-model", ""}, {"--reviewer-effort", ""}} {
		t.Run(args[0], func(t *testing.T) {
			cmd := &cobra.Command{}
			var reviewer, model, effort string
			bindReviewerFlags(cmd, &reviewer, &model, &effort)
			if err := cmd.ParseFlags(args); err != nil {
				t.Fatal(err)
			}
			if _, err := reviewerFromFlags(cmd, reviewer, model, effort); err == nil {
				t.Fatalf("reviewerFromFlags(%v) succeeded", args)
			}
		})
	}
}
