package engine

import (
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/store"
)

func TestRetryFindingsIgnoreOlderArtifactVersion(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateTask(domain.Task{ID: "A", Validator: "ok", ArtifactPath: "a.txt", MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Claim("A", "r1", "fake"); !ok {
		t.Fatal("claim 1")
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunSucceeded, AllowRetry: true,
		Evidence: domain.Evidence{ArtifactPath: "a.txt", ArtifactVersion: "v1"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddReview(domain.Review{TaskID: "A", ArtifactVersion: "v1", Kind: domain.ReviewFull,
		Findings: []domain.Finding{{Severity: domain.SeverityP1, Code: "old", Message: "old defect"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Claim("A", "r2", "fake"); !ok {
		t.Fatal("claim 2")
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r2", RunStatus: domain.RunSucceeded,
		Note: `verification: missing "DONE"`, Evidence: domain.Evidence{ArtifactPath: "a.txt", ArtifactVersion: "v2"}}); err != nil {
		t.Fatal(err)
	}
	task, _, _ := st.Get("A")
	e := New(st, exec.NewFake(1), 1, nil)
	findings, err := e.retryFindings(task)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("stale findings leaked into retry: %+v", findings)
	}
}
