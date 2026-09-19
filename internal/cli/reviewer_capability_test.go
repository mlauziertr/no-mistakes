package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/daemon"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestExplicitReviewerRequiresDaemonCapabilityBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	p := paths.WithRoot(makeSocketSafeTempDir(t))
	t.Setenv("NM_HOME", p.Root())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	cliGit(t, dir, "init", "-b", "main")
	cliGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	chdir(t, dir)
	root, err := git.FindGitRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.InsertRepo(root, "https://example.com/repo.git", "main"); err != nil {
		t.Fatal(err)
	}

	srv := ipc.NewServer()
	var claims atomic.Int32
	var reruns atomic.Int32
	srv.Handle(ipc.MethodHealth, func(context.Context, json.RawMessage) (interface{}, error) {
		return &ipc.HealthResult{Status: "ok"}, nil
	})
	srv.Handle(ipc.MethodClaimLaunchReceipt, func(context.Context, json.RawMessage) (interface{}, error) {
		claims.Add(1)
		return nil, errors.New("legacy claim reached")
	})
	srv.Handle(ipc.MethodRerun, func(context.Context, json.RawMessage) (interface{}, error) {
		reruns.Add(1)
		return &ipc.RerunResult{RunID: "rerun-1"}, nil
	})
	done := make(chan error, 1)
	go func() { done <- srv.Serve(p.Socket()) }()
	t.Cleanup(func() {
		srv.Close()
		<-done
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if alive, _ := daemon.IsRunning(p); alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("test IPC server did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	reviewer := &config.ReviewAgent{Agent: types.AgentPi}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&bytes.Buffer{})
	if err := runAxiRunWithLaunchProofAndReviewer(cmd, false, nil, "intent", "", "reviewer-capability", "generation", time.Second, reviewer); err == nil || !strings.Contains(err.Error(), "resolve reviewer") {
		t.Fatalf("AXI old-daemon error = %v", err)
	}
	if claims.Load() != 0 {
		t.Fatalf("old daemon received %d launch claims before reviewer capability rejection", claims.Load())
	}

	rerunCmd := newRerunCmd()
	rerunCmd.SetArgs([]string{"--reviewer", "pi"})
	rerunCmd.SetOut(&bytes.Buffer{})
	if err := rerunCmd.Execute(); err == nil || !strings.Contains(err.Error(), "resolve reviewer") {
		t.Fatalf("rerun old-daemon error = %v", err)
	}
	if reruns.Load() != 0 {
		t.Fatalf("old daemon received %d rerun requests before reviewer capability rejection", reruns.Load())
	}

	legacy := &cobra.Command{}
	legacy.SetContext(context.Background())
	legacy.SetOut(&bytes.Buffer{})
	if err := runAxiRunWithLaunchProof(legacy, false, nil, "intent", "", "legacy-capability", "generation", time.Second); err == nil || !strings.Contains(err.Error(), "legacy claim reached") {
		t.Fatalf("legacy AXI request error = %v", err)
	}
	if claims.Load() != 1 {
		t.Fatalf("legacy request made %d launch claims, want 1", claims.Load())
	}

	legacyRerun := newRerunCmd()
	legacyRerun.SetOut(&bytes.Buffer{})
	if err := legacyRerun.Execute(); err != nil {
		t.Fatalf("legacy rerun failed: %v", err)
	}
	if reruns.Load() != 1 {
		t.Fatalf("legacy request made %d reruns, want 1", reruns.Load())
	}
}
