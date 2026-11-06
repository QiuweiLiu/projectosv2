package store_test

import (
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/store"
)

func TestMetricsSummarisesRunsAndReviews(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "ok", ObjectiveID: "obj-1", PlanVersion: 1}); err != nil {
		t.Fatal(err)
	}
	run, ok, err := st.Claim("T1", "r1", "fake")
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(store.Settlement{RunID: run.ID, RunStatus: domain.RunSucceeded,
		Accepted: true, Result: "done", PromptTokens: 10, OutputTokens: 5}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddReview(domain.Review{TaskID: "T1", ArtifactVersion: "v1",
		Kind: domain.ReviewFull, Findings: []domain.Finding{{Severity: domain.SeverityP1, Code: "x"}}}); err != nil {
		t.Fatal(err)
	}
	m, err := st.Metrics()
	if err != nil {
		t.Fatal(err)
	}
	if m.Tasks != 1 || m.Accepted != 1 || m.Runs != 1 || m.Reviews != 1 || m.BlockingReviews != 1 {
		t.Fatalf("metrics = %+v", m)
	}
	if m.PromptTokens != 10 || m.OutputTokens != 5 {
		t.Fatalf("tokens = %+v", m)
	}
}
