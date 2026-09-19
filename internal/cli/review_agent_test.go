package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/no-mistakes/internal/agentcfg"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestReviewerFromFlagsUsesExplicitPiReviewer(t *testing.T) {
	cmd := &cobra.Command{}
	var reviewer, model, effort string
	bindReviewerFlags(cmd, &reviewer, &model, &effort)
	if err := cmd.ParseFlags([]string{"--reviewer", "pi", "--reviewer-model", "xai/grok-4.6", "--reviewer-effort", "high"}); err != nil {
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

func TestReviewerFromFlagsRequiresExplicitReviewerForTuning(t *testing.T) {
	for _, args := range [][]string{
		{"--reviewer-model", "xai/grok-4.6"},
		{"--reviewer-effort", "high"},
		{"--reviewer-model", "xai/grok-4.6", "--reviewer-effort", "high"},
	} {
		cmd := &cobra.Command{}
		var reviewer, model, effort string
		bindReviewerFlags(cmd, &reviewer, &model, &effort)
		if err := cmd.ParseFlags(args); err != nil {
			t.Fatal(err)
		}
		if _, err := reviewerFromFlags(cmd, reviewer, model, effort); err == nil || !strings.Contains(err.Error(), "--reviewer is required") {
			t.Fatalf("reviewerFromFlags(%v) error = %v", args, err)
		}
	}
}

func TestReviewerFromFlagsNoOverride(t *testing.T) {
	cmd := &cobra.Command{}
	var reviewer, model, effort string
	bindReviewerFlags(cmd, &reviewer, &model, &effort)
	got, err := reviewerFromFlags(cmd, reviewer, model, effort)
	if err != nil || got != nil {
		t.Fatalf("reviewer = %#v, error = %v", got, err)
	}
}

func TestReviewerFromFlagsRejectsCredentialShapedModel(t *testing.T) {
	secret := strings.Repeat("a", 32)
	for _, args := range [][]string{
		{"--reviewer", "pi", "--reviewer-model", "https://secret@example.test/model"},
		{"--reviewer", "pi", "--reviewer-model", "openai/sk-" + "proj-" + secret},
		{"--reviewer", "codex", "--reviewer-model", "provider/sk-" + "proj-" + secret},
		{"--reviewer", "codex", "--reviewer-model", "xai-" + secret},
		{"--reviewer", "codex", "--reviewer-model", "hf_" + secret},
		{"--reviewer", "pi", "--reviewer-model", "xai/glpat-" + secret},
	} {
		cmd := &cobra.Command{}
		var reviewer, model, effort string
		bindReviewerFlags(cmd, &reviewer, &model, &effort)
		if err := cmd.ParseFlags(args); err != nil {
			t.Fatal(err)
		}
		if _, err := reviewerFromFlags(cmd, reviewer, model, effort); err == nil {
			t.Fatal("credential-shaped reviewer model was accepted")
		} else if strings.Contains(err.Error(), secret) {
			t.Fatal("rejected reviewer model was echoed")
		}
	}
}

func TestReviewerFromFlagsRejectsEmptyExplicitValues(t *testing.T) {
	for _, args := range [][]string{{"--reviewer", ""}, {"--reviewer", "pi", "--reviewer-model", ""}, {"--reviewer", "pi", "--reviewer-effort", ""}} {
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
