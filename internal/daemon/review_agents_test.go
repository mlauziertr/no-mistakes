package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/runenv"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestResolveReviewAgentRPCNormalizesSelection(t *testing.T) {
	p, _ := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{&mockPassStep{name: types.StepReview}}
	})
	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request := &config.ReviewAgent{Agent: types.AgentPi, Model: " xai/grok-4.6 "}
	var resolved config.ReviewAgent
	if err := client.Call(ipc.MethodResolveReviewAgent, request, &resolved); err != nil {
		t.Fatal(err)
	}
	want := &config.ReviewAgent{Agent: types.AgentPi, Model: "xai/grok-4.6"}
	if !config.ReviewAgentsEqual(&resolved, want) {
		t.Fatalf("resolved reviewer = %#v, want %#v", resolved, *want)
	}
}

func TestPipelineReviewRolesUseIndependentPiProfiles(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "pi")
	const response = `{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"ok"}]}]}`
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > pi-argv.txt\ncat >/dev/null\nprintf '%s\\n' '" + response + "'\n"
	if runtime.GOOS == "windows" {
		bin += ".cmd"
		script = "@echo off\r\necho %* > pi-argv.txt\r\nmore > nul\r\necho " + response + "\r\n"
	}
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	global, err := config.LoadGlobalFromBytes([]byte(`agent: pi
agent_config:
  pi: {model: default-model, effort: high}
review_agents:
  reviewer: {agent: pi, model: anthropic-vertex/claude-opus-4-8, effort: max}
  fixer: {agent: pi, model: google-vertex/gemini-3.8-flash, effort: max}
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Merge(global, &config.RepoConfig{})
	cfg.AgentPathOverride = map[string]string{"pi": bin}
	cfg.DisableProjectSettings = true
	ag, err := newPipelineAgent(context.Background(), cfg, t.TempDir(), fakeLookPath, runenv.Overlay{})
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	for _, tc := range []struct{ purpose, model, effort string }{
		{"review", "anthropic-vertex/claude-opus-4-8", "max"},
		{"review-fix", "google-vertex/gemini-3.8-flash", "max"},
		{"review", "anthropic-vertex/claude-opus-4-8", "max"},
		{"test-evidence", "default-model", "high"},
	} {
		_, err := ag.Run(context.Background(), agent.RunOpts{Purpose: tc.purpose, Prompt: "hello", CWD: dir})
		if err != nil {
			t.Fatal(err)
		}
		args, err := os.ReadFile(filepath.Join(dir, "pi-argv.txt"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"--model " + tc.model, "--thinking " + tc.effort, "--no-context-files"} {
			if !strings.Contains(string(args), want) {
				t.Fatalf("%s args %q missing %q", tc.purpose, args, want)
			}
		}
	}
}

func TestPipelineReviewRoleFailsClosed(t *testing.T) {
	cfg := &config.Config{Agent: types.AgentPi, DisableProjectSettings: true,
		ReviewAgents: map[string]config.ReviewAgent{"reviewer": {Agent: types.AgentAntigravity}}}
	_, err := newPipelineAgent(context.Background(), cfg, t.TempDir(), fakeLookPath, runenv.Overlay{})
	if err == nil || !strings.Contains(err.Error(), "review_agents.reviewer") || !strings.Contains(err.Error(), "does not neutralize") {
		t.Fatalf("unsafe reviewer error = %v", err)
	}
}

func TestInvalidReviewerLaunchDoesNotSupersedeActiveRun(t *testing.T) {
	for _, tc := range []struct {
		name        string
		global      func(string, string) string
		trusted     string
		reviewer    config.ReviewAgent
		wantFailure string
	}{
		{
			name: "reviewer binary unavailable",
			global: func(fake, missing string) string {
				return "agent: claude\nagent_path_override:\n  claude: " + fake + "\n  codex: " + missing + "\n"
			},
			reviewer:    config.ReviewAgent{Agent: types.AgentCodex},
			wantFailure: "codex",
		},
		{
			name: "trusted project instruction opt-out rejects reviewer",
			global: func(fake, _ string) string {
				return "agent: claude\nagent_path_override:\n  claude: " + fake + "\n  grok: " + fake + "\n"
			},
			trusted:     "disable_project_settings: true\n",
			reviewer:    config.ReviewAgent{Agent: types.AgentGrok},
			wantFailure: "does not neutralize",
		},
		{
			name:        "invalid global configuration",
			global:      func(_, _ string) string { return "agent: [\n" },
			reviewer:    config.ReviewAgent{Agent: types.AgentPi},
			wantFailure: "load global config",
		},
		{
			name: "cursor alias rejected before mutation",
			global: func(fake, _ string) string {
				return "agent: claude\nagent_path_override:\n  claude: " + fake + "\n"
			},
			reviewer:    config.ReviewAgent{Agent: types.AgentCursor},
			wantFailure: "not supported for per-run reviewer selection",
		},
		{
			name: "cursor ACP target rejected before mutation",
			global: func(fake, _ string) string {
				return "agent: claude\nagent_path_override:\n  claude: " + fake + "\n"
			},
			reviewer:    config.ReviewAgent{Agent: "acp:cursor"},
			wantFailure: "not supported for per-run reviewer selection",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := paths.WithRoot(t.TempDir())
			if err := p.EnsureDirs(); err != nil {
				t.Fatal(err)
			}
			d, err := db.Open(p.DB())
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			repo, head := setupTestGitRepo(t, p, d, "invalid-reviewer-launch")
			if tc.trusted != "" {
				head = commitDefaultBranchConfig(t, repo.WorkingPath, tc.trusted)
			}
			active, err := d.InsertRun(repo.ID, "main", head, head)
			if err != nil {
				t.Fatal(err)
			}
			fake := writeMockClaude(t, t.TempDir())
			missing := filepath.Join(t.TempDir(), "missing-reviewer")
			if err := os.WriteFile(p.ConfigFile(), []byte(tc.global(fake, missing)), 0o600); err != nil {
				t.Fatal(err)
			}
			m := NewRunManager(d, p, nil)
			cancelled := false
			m.cancels[active.ID] = func(error) { cancelled = true }
			if _, err := m.startRunWithReviewer(context.Background(), repo, "main", head, head, "test", nil, "intent", "", &tc.reviewer); err == nil {
				t.Fatal("invalid reviewer launch succeeded")
			} else if !strings.Contains(err.Error(), tc.wantFailure) {
				t.Fatalf("launch error = %v, want %q", err, tc.wantFailure)
			}
			runs, err := d.GetRunsByRepo(repo.ID)
			if err != nil {
				t.Fatal(err)
			}
			if cancelled || len(runs) != 1 || runs[0].ID != active.ID {
				t.Fatalf("invalid reviewer changed active validation: cancelled=%v runs=%#v", cancelled, runs)
			}
		})
	}
}

// reviewRoleProbeStep drives one review turn and one review-fix turn through the
// pipeline agent the executor was given, so a test can observe which harness
// profile each role resolved to. Sessions are deliberately not used: pi requires
// a session-id header when resuming, which the fake binary does not emit.
type reviewRoleProbeStep struct{}

func (s *reviewRoleProbeStep) Name() types.StepName { return types.StepReview }

func (s *reviewRoleProbeStep) Execute(sctx *pipeline.StepContext) (*pipeline.StepOutcome, error) {
	if _, err := sctx.RunAgentContext(sctx.Ctx, agent.RunOpts{Purpose: "review", Prompt: "review", CWD: sctx.WorkDir}); err != nil {
		return nil, err
	}
	if _, err := sctx.RunAgentContext(sctx.Ctx, agent.RunOpts{Purpose: "review-fix", Prompt: "fix", CWD: sctx.WorkDir}); err != nil {
		return nil, err
	}
	return &pipeline.StepOutcome{}, nil
}

// writeCapturingPiAgent writes a fake pi binary that appends its argv to
// capturePath on every invocation and returns a minimal agent_end payload.
func writeCapturingPiAgent(t *testing.T, dir, capturePath string) string {
	t.Helper()
	const response = `{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"ok"}]}]}`
	bin := filepath.Join(dir, "pi")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuoteForTest(capturePath) + "\ncat >/dev/null\nprintf '%s\\n' '" + response + "'\n"
	if runtime.GOOS == "windows" {
		bin += ".cmd"
		script = "@echo off\r\necho %* >> \"" + capturePath + "\"\r\nmore > nul\r\necho " + response + "\r\n"
	}
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestPushReceivedRoutesReviewRolesToIndependentProfiles is the behavioral
// regression for review-role routing on the NORMAL push start path
// (HandlePushReceived -> startRun -> startRunWithIntentSourceLocked), which used
// to build the pipeline agent inline and never apply WithReviewAgents. Both role
// models can only appear in the captured argv if that wrapper is applied on this
// path; before the fix both review-loop turns ran on the default profile.
func TestPushReceivedPersistsExplicitReviewerSelection(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "pi-argv.log")
	piBin := writeCapturingPiAgent(t, t.TempDir(), capturePath)

	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{&reviewRoleProbeStep{}}
	})
	configYAML := "agent: pi\n" +
		"agent_path_override:\n  pi: " + piBin + "\n" +
		"agent_config:\n  pi: {model: author-model, effort: high}\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	repo, headSHA := setupTestGitRepo(t, p, d, "reviewer-selection-repo")
	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	selection := &config.ReviewAgent{Agent: types.AgentPi, Model: "xai/grok-4.6"}
	var result ipc.PushReceivedResult
	if err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir(repo.ID), Ref: "refs/heads/main", Old: strings.Repeat("0", 40), New: headSHA, Reviewer: selection,
	}, &result); err != nil {
		t.Fatal(err)
	}
	if run := waitForRunTerminalState(t, d, result.RunID); run.Status != types.RunCompleted {
		t.Fatalf("run status = %q, want completed (error: %v)", run.Status, run.Error)
	}
	stored, err := d.GetRun(result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ReviewAgentJSON == nil {
		t.Fatal("run did not persist reviewer selection")
	}
	got, err := config.ParseReviewAgentJSON(*stored.ReviewAgentJSON)
	if err != nil || !config.ReviewAgentsEqual(got, selection) {
		t.Fatalf("stored reviewer = %#v, parse error = %v", got, err)
	}
	argv, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "--model xai/grok-4.6") {
		t.Fatalf("reviewer model was not routed; argv = %q", argv)
	}
}

func TestPushReceivedRoutesReviewRolesToIndependentProfiles(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "pi-argv.log")
	piBin := writeCapturingPiAgent(t, t.TempDir(), capturePath)

	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{&reviewRoleProbeStep{}}
	})

	configYAML := "agent: pi\n" +
		"agent_path_override:\n  pi: " + piBin + "\n" +
		"agent_config:\n  pi: {model: default-model, effort: high}\n" +
		"review_agents:\n" +
		"  reviewer: {agent: pi, model: anthropic-vertex/claude-opus-4-8, effort: max}\n" +
		"  fixer: {agent: pi, model: google-vertex/gemini-3.8-flash, effort: max}\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	_, headSHA := setupTestGitRepo(t, p, d, "review-roles-run-repo")

	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var result ipc.PushReceivedResult
	if err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("review-roles-run-repo"),
		Ref:  "refs/heads/main",
		Old:  "0000000000000000000000000000000000000000",
		New:  headSHA,
	}, &result); err != nil {
		t.Fatal(err)
	}

	run := waitForRunTerminalState(t, d, result.RunID)
	if run.Status != types.RunCompleted {
		var runErr string
		if run.Error != nil {
			runErr = *run.Error
		}
		t.Fatalf("run status = %q, want %q (error: %s)", run.Status, types.RunCompleted, runErr)
	}

	argv, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(argv)
	for role, want := range map[string]string{
		"reviewer": "--model anthropic-vertex/claude-opus-4-8",
		"fixer":    "--model google-vertex/gemini-3.8-flash",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("%s profile not routed on push start path; argv = %q missing %q", role, got, want)
		}
	}
}
