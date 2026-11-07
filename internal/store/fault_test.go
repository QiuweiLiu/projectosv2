package store_test

import (
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

func TestClosedStoreCannotSettleRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kernel.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "ok", ArtifactPath: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.Claim("T1", "r1", "fake"); err != nil || !ok {
		t.Fatal(err, ok)
	}
	st.Close()
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunSucceeded,
		Accepted: true, Result: "done"}); err == nil {
		t.Fatal("closed store accepted a settlement")
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	task, _, _ := reopened.Get("T1")
	if task.Status != domain.TaskBlocked {
		t.Fatalf("interrupted run was not blocked: %+v", task)
	}
	evidence, err := reopened.EvidenceFor("T1")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evidence {
		if ev.Accepted {
			t.Fatalf("failed settlement created accepted evidence: %+v", ev)
		}
	}
}

func TestClosedStoreCannotRecordReview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kernel.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "ok", ArtifactPath: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.Claim("T1", "r1", "fake"); err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunSucceeded,
		Accepted: true, Result: "done"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := st.AddReview(domain.Review{TaskID: "T1", ArtifactVersion: "v1", Kind: domain.ReviewFull}); err == nil {
		t.Fatal("closed store accepted a review")
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	task, _, _ := reopened.Get("T1")
	if task.Status != domain.TaskAccepted {
		t.Fatalf("failed review write changed task state: %+v", task)
	}
	reviews, err := reopened.ReviewsFor("T1")
	if err != nil || len(reviews) != 0 {
		t.Fatalf("reviews = %+v %v", reviews, err)
	}
}

func TestObjectiveStatusWriteFailureDoesNotClaimCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kernel.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	obj := goal.Objective{ID: "obj-1", Goal: "g", Status: goal.StatusActive, PlanVersion: 1}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := st.MoveObjective("obj-1", goal.StatusCompleted); err == nil {
		t.Fatal("closed store reported a completion transition")
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, ok, _ := reopened.GetObjective("obj-1")
	if !ok || got.Status != goal.StatusActive {
		t.Fatalf("objective = %+v, want active", got)
	}
}
