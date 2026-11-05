package engine

import (
	"context"
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/store"
)

// A retry must continue the provider session that produced the failed attempt;
// otherwise the worker loses everything it learned before the review.
func TestRetryResumesPreviousSession(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateTask(domain.Task{ID: "T1", Prompt: "p", Validator: "ok", MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	run, ok, err := st.Claim("T1", "r1", "fake")
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(store.Settlement{
		RunID: run.ID, RunStatus: domain.RunFailed, AllowRetry: true, SessionID: "ses-prev",
	}); err != nil {
		t.Fatal(err)
	}

	fake := exec.NewFake(1)
	fake.Output["T1"] = "done"
	e := New(st, fake, 1, map[string]ValidatorFunc{"ok": func(string, map[domain.TaskID]string) error { return nil }})
	if _, err := e.Run(context.Background(), 20); err != nil {
		t.Fatal(err)
	}
	if len(fake.Starts) != 1 {
		t.Fatalf("starts = %d", len(fake.Starts))
	}
	if fake.Starts[0].SessionID != "ses-prev" {
		t.Fatalf("session = %q, want ses-prev", fake.Starts[0].SessionID)
	}
}
