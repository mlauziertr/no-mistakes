package db

import "testing"

func TestRunReviewerSelectionIsWriteOnce(t *testing.T) {
	d := openTestDB(t)
	repo, err := d.InsertRepo("/home/user/reviewer-project", "git@github.com:user/reviewer-project.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	selection := `{"agent":"pi","model":"xai/grok-4.6"}`
	run, err := d.InsertRunWithIntentAndLaunchNonce(repo.ID, "feature", "head", "base", nil, "nonce", "generation", "digest", "", selection)
	if err != nil {
		t.Fatal(err)
	}
	if run.ReviewAgentJSON == nil || *run.ReviewAgentJSON != selection {
		t.Fatalf("inserted reviewer = %v, want %q", run.ReviewAgentJSON, selection)
	}
	if _, err := d.sql.Exec(`UPDATE runs SET review_agent_json = ? WHERE id = ?`, `{"agent":"pi","model":"other"}`, run.ID); err == nil {
		t.Fatal("different reviewer selection was accepted")
	}
	conflict := `{"agent":"pi","model":"other"}`
	got, claimed, err := d.ClaimLaunchReceipt(repo.ID, "feature", "nonce", "head", "generation", "digest", "", conflict)
	if err != nil || claimed || got == nil || got.LaunchReceiptClaimedAt != nil {
		t.Fatalf("conflicting reviewer claim = %+v, claimed=%v, err=%v", got, claimed, err)
	}
	got, claimed, err = d.ClaimLaunchReceipt(repo.ID, "feature", "nonce", "head", "generation", "digest", "", selection)
	if err != nil || !claimed || got == nil || got.LaunchReceiptClaimedAt == nil {
		t.Fatalf("matching reviewer claim = %+v, claimed=%v, err=%v", got, claimed, err)
	}
	got, err = d.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReviewAgentJSON == nil || *got.ReviewAgentJSON != selection {
		t.Fatalf("stored reviewer = %v, want %q", got.ReviewAgentJSON, selection)
	}
}
