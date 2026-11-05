package store_test

import (
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/store"
)

func TestUsageTokensPrefersProviderCounts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "ok"}); err != nil {
		t.Fatal(err)
	}
	run, ok, err := st.Claim("T1", "r1", "fake")
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(store.Settlement{
		RunID: run.ID, RunStatus: domain.RunSucceeded, Accepted: true,
		PromptChars: 4000, OutputChars: 4000, // deliberately wrong characters
		PromptTokens: 100, OutputTokens: 50, UsageEstimated: false,
	}); err != nil {
		t.Fatal(err)
	}
	p, o, estimated, err := st.UsageTokens()
	if err != nil {
		t.Fatal(err)
	}
	if p != 100 || o != 50 || estimated {
		t.Fatalf("usage = %d/%d estimated=%v", p, o, estimated)
	}
}

func TestUsageTokensFallsBackToCharacters(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "ok"}); err != nil {
		t.Fatal(err)
	}
	run, ok, err := st.Claim("T1", "r1", "fake")
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(store.Settlement{
		RunID: run.ID, RunStatus: domain.RunSucceeded, Accepted: true,
		PromptChars: 400, OutputChars: 80,
	}); err != nil {
		t.Fatal(err)
	}
	p, o, estimated, err := st.UsageTokens()
	if err != nil {
		t.Fatal(err)
	}
	if p != 100 || o != 20 || !estimated {
		t.Fatalf("fallback = %d/%d estimated=%v", p, o, estimated)
	}
}

func TestLastSessionReturnsMostRecent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "ok", MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	run, ok, err := st.Claim("T1", "r1", "fake")
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(store.Settlement{
		RunID: run.ID, RunStatus: domain.RunFailed, AllowRetry: true, SessionID: "ses-1",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.LastSession("T1", "fake")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ses-1" {
		t.Fatalf("session = %q", got)
	}
}
