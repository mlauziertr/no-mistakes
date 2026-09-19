package db

import "testing"

func TestRunReviewerSelectionIsWriteOnce(t *testing.T) {
	d := openTestDB(t)
	repo, err := d.InsertRepo("/home/user/reviewer-project", "git@github.com:user/reviewer-project.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "feature", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	selection := `{"agent":"pi","model":"xai/grok-4.6"}`
	if err := d.SetRunReviewAgent(run.ID, selection); err != nil {
		t.Fatal(err)
	}
	if err := d.SetRunReviewAgent(run.ID, selection); err != nil {
		t.Fatalf("same selection was not idempotent: %v", err)
	}
	if err := d.SetRunReviewAgent(run.ID, `{"agent":"pi","model":"other"}`); err == nil {
		t.Fatal("different reviewer selection was accepted")
	}
	got, err := d.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReviewAgentJSON == nil || *got.ReviewAgentJSON != selection {
		t.Fatalf("stored reviewer = %v, want %q", got.ReviewAgentJSON, selection)
	}
}
