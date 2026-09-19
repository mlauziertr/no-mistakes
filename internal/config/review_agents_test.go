package config

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agentcfg"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestReviewAgentsProfilesAreIndependent(t *testing.T) {
	global := writeGlobalConfig(t, `agent: codex
agent_config:
  pi: {model: default-model, effort: high}
review_agents:
  reviewer: {agent: pi, model: review-model, effort: max}
  fixer: {agent: pi, model: fix-model}
`)
	cfg := Merge(global, &RepoConfig{})
	reviewer := cfg.ForReviewAgent(cfg.ReviewAgents["reviewer"])
	fixer := cfg.ForReviewAgent(cfg.ReviewAgents["fixer"])
	if got := reviewer.AgentProfile(); got != (agentcfg.Profile{Model: "review-model", Effort: agentcfg.EffortMax}) {
		t.Fatalf("reviewer = %+v", got)
	}
	if got := fixer.AgentProfile(); got != (agentcfg.Profile{Model: "fix-model", Effort: agentcfg.EffortHigh}) {
		t.Fatalf("fixer = %+v", got)
	}
	if cfg.Agent != types.AgentCodex || cfg.AgentProfileFor(types.AgentPi).Model != "default-model" {
		t.Fatal("role selection mutated default configuration")
	}
}

func TestReviewAgentsRejectInvalidConfig(t *testing.T) {
	for _, input := range []string{
		"review_agents: {other: {agent: pi}}",
		"review_agents: {reviewer: {model: x}}",
		"review_agents: {reviewer: {agent: auto}}",
		"review_agents: {fixer: {agent: unknown}}",
		"review_agents: {reviewer: {agent: pi, effort: turbo}}",
		"review_agents: {fixer: {agent: cursor, effort: max}}",
		"review_agents: {fixer: {agent: rovodev, model: x}}",
		"review_agents: {reviewer: {agent: pi, typo: x}}",
	} {
		t.Run(input, func(t *testing.T) { loadGlobalConfigError(t, input) })
	}
}

func TestRepositoryCannotSelectReviewAgents(t *testing.T) {
	repo, err := LoadRepoFromBytes([]byte("review_agents: {reviewer: {agent: pi, model: attacker-model}}"))
	if err != nil {
		t.Fatal(err)
	}
	global := writeGlobalConfig(t, "review_agents: {reviewer: {agent: pi, model: operator-model}}")
	cfg := Merge(global, repo)
	if cfg.ReviewAgents["reviewer"].Model != "operator-model" {
		t.Fatal("repository changed operator profile")
	}
	if Merge(DefaultGlobalConfig(), repo).ReviewAgents != nil {
		t.Fatal("repository selected a role")
	}
}

func TestReviewAgentsOmitted(t *testing.T) {
	cfg := Merge(writeGlobalConfig(t, "agent: pi\n"), &RepoConfig{})
	if cfg.ReviewAgents != nil {
		t.Fatalf("unexpected roles: %+v", cfg.ReviewAgents)
	}
}

func TestReviewAgentSnapshotAcceptsCatalogIDsForNonPiHarnesses(t *testing.T) {
	entry := &ReviewAgent{Agent: types.AgentCodex, Model: "gpt-5.6-sol", Effort: agentcfg.EffortMedium}
	encoded, err := MarshalReviewAgent(entry)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseReviewAgentJSON(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !ReviewAgentsEqual(got, entry) {
		t.Fatalf("round trip = %#v, want %#v", got, entry)
	}
	if _, err := MarshalReviewAgent(&ReviewAgent{Agent: types.AgentCodex, Model: "https://secret@example.test/model"}); err == nil {
		t.Fatal("credential-shaped non-Pi reviewer model was accepted")
	}
}

func TestReviewAgentSnapshotRejectsCredentialShapedModelWithoutEcho(t *testing.T) {
	tests := []struct {
		agent  types.AgentName
		model  string
		secret string
	}{
		{types.AgentCodex, "sk-" + "proj-" + strings.Repeat("a", 32), strings.Repeat("a", 32)},
		{types.AgentCodex, "provider/github_" + "pat_" + strings.Repeat("b", 40), strings.Repeat("b", 40)},
		{types.AgentCodex, "provider/ghp_" + strings.Repeat("c", 36), strings.Repeat("c", 36)},
		{types.AgentPi, "openai/sk-" + "proj-" + strings.Repeat("d", 32), strings.Repeat("d", 32)},
		{types.AgentPi, "openai/github_" + "pat_" + strings.Repeat("e", 40), strings.Repeat("e", 40)},
		{types.AgentCodex, "xai-" + strings.Repeat("f", 80), strings.Repeat("f", 80)},
		{types.AgentCodex, "hf_" + strings.Repeat("g", 32), strings.Repeat("g", 32)},
		{types.AgentCodex, "AIza" + strings.Repeat("i", 35), strings.Repeat("i", 35)},
		{types.AgentPi, "google/AIza" + strings.Repeat("j", 35), strings.Repeat("j", 35)},
		{types.AgentCodex, "sk_live_" + strings.Repeat("k", 32), strings.Repeat("k", 32)},
		{types.AgentPi, "stripe/rk_live_" + strings.Repeat("m", 32), strings.Repeat("m", 32)},
		{types.AgentCodex, "npm_" + strings.Repeat("n", 36), strings.Repeat("n", 36)},
		{types.AgentPi, "registry/npm_" + strings.Repeat("o", 36), strings.Repeat("o", 36)},
		{types.AgentCodex, "ASIA" + strings.Repeat("P", 16), strings.Repeat("P", 16)},
		{types.AgentPi, "aws/ASIA" + strings.Repeat("Q", 16), strings.Repeat("Q", 16)},
		{types.AgentCodex, "xapp-1-" + strings.Repeat("r", 32), strings.Repeat("r", 32)},
		{types.AgentPi, "slack/xapp-1-" + strings.Repeat("s", 32), strings.Repeat("s", 32)},
	}
	for _, prefix := range []string{"glpat-", "gloas-", "gldt-", "glrt-", "glrtr-", "glcbt-", "glptt-", "glft-", "glimt-", "glagent-", "glwt-", "glsoat-", "glffct-"} {
		secret := strings.Repeat("h", 32)
		tests = append(tests, struct {
			agent  types.AgentName
			model  string
			secret string
		}{types.AgentPi, "xai/" + prefix + secret, secret})
	}
	for _, test := range tests {
		_, err := MarshalReviewAgent(&ReviewAgent{Agent: test.agent, Model: test.model})
		if err == nil {
			t.Fatal("credential-shaped reviewer model was accepted")
		}
		if strings.Contains(err.Error(), test.secret) {
			t.Fatal("rejected reviewer model was echoed")
		}
		_, err = LoadGlobalFromBytes([]byte("review_agents:\n  reviewer: {agent: " + string(test.agent) + ", model: " + test.model + "}\n"))
		if err == nil {
			t.Fatal("credential-shaped configured reviewer model was accepted")
		}
		if strings.Contains(err.Error(), test.secret) {
			t.Fatal("rejected configured reviewer model was echoed")
		}
	}
}

func TestReviewAgentSnapshotRoundTripsStrictly(t *testing.T) {
	entry := &ReviewAgent{Agent: types.AgentPi, Model: " xai/grok-4.6 ", Effort: agentcfg.EffortHigh}
	encoded, err := MarshalReviewAgent(entry)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseReviewAgentJSON(encoded)
	if err != nil {
		t.Fatal(err)
	}
	want := &ReviewAgent{Agent: types.AgentPi, Model: "xai/grok-4.6", Effort: agentcfg.EffortHigh}
	if !ReviewAgentsEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
	for _, invalid := range []string{
		`{"agent":"pi","model":"xai/grok-4.6","unexpected":true}`,
		`{"agent":"not-a-harness","model":"xai/grok-4.6"}`,
	} {
		if _, err := ParseReviewAgentJSON(invalid); err == nil {
			t.Fatalf("ParseReviewAgentJSON(%s) succeeded", invalid)
		}
	}
}
