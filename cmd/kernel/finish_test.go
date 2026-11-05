package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
	"projectos/kernel/internal/verify"
)

// seedActiveGoal writes a confirmed one-task objective and returns its dir.
func seedActiveGoal(t *testing.T, dir string) {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	obj := goal.Objective{ID: "obj-1", Goal: "write a.txt", Status: goal.StatusActive,
		PlanVersion: 1, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
	plan := goal.Plan{Tasks: []goal.PlanTask{{
		ID: "T1", Goal: "write a.txt", Artifacts: []string{"a.txt"}, Risk: "low",
		Validators: []verify.Spec{{Type: "nonempty"}},
	}}}
	if _, err := st.CreatePlan(obj.ID, 1, plan, "hash-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmPlan(obj.ID, 1); err != nil {
		t.Fatal(err)
	}
	st.Close()
}

// A snapshot failure must not be reported as completion, and the objective must
// stay re-runnable.
func TestFinishGoalStaysActiveWhenSnapshotFails(t *testing.T) {
	dir := t.TempDir()
	seedActiveGoal(t, dir)

	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "nonempty", ArtifactPath: "a.txt",
		ObjectiveID: "obj-1", PlanVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Claim("T1", "r1", "fake"); !ok {
		t.Fatal("claim")
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunSucceeded, Accepted: true, Result: "hello"}); err != nil {
		t.Fatal(err)
	}
	obj, _, _ := st.GetObjective("obj-1")
	tasks, _ := st.List()

	// A file where the snapshot directory belongs makes the snapshot write fail.
	if err := os.WriteFile(filepath.Join(dir, ".project"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := finishGoal(st, obj, tasks, dir); code == exitOK {
		t.Fatal("completion was reported even though the snapshot failed")
	}
	after, _, _ := st.GetObjective("obj-1")
	if after.Status == goal.StatusCompleted {
		t.Fatal("objective was marked completed before the snapshot succeeded")
	}
}

// A cancel flag left by a finished scheduler must not cancel the next run.
func TestStaleCancelFlagDoesNotCancelNextRun(t *testing.T) {
	dir := t.TempDir()
	seedActiveGoal(t, dir)

	flag := filepath.Join(dir, "cancel")
	if err := os.WriteFile(flag, []byte("stale request\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code := goalRun([]string{
		"--dir", dir, "--executor", "fake", "--interval", "1ms", "--timeout", "10s", "--max-runs", "5",
	})
	if code != exitOK {
		t.Fatalf("run = %d, want success", code)
	}
	if _, err := os.Stat(flag); !os.IsNotExist(err) {
		t.Fatal("stale cancel flag was not cleared")
	}

	st, err := store.OpenReadOnly(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	obj, ok, err := st.GetObjective("obj-1")
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if obj.Status != goal.StatusCompleted {
		t.Fatalf("objective = %s, want completed", obj.Status)
	}
}

// A failed delivery report must leave the objective active so it can be rerun.
func TestFinishGoalStaysActiveWhenDeliveryWriteFails(t *testing.T) {
	dir := t.TempDir()
	seedActiveGoal(t, dir)

	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "nonempty", ArtifactPath: "a.txt",
		ObjectiveID: "obj-1", PlanVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Claim("T1", "r1", "fake"); !ok {
		t.Fatal("claim")
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunSucceeded, Accepted: true, Result: "hello"}); err != nil {
		t.Fatal(err)
	}
	obj, _, _ := st.GetObjective("obj-1")
	tasks, _ := st.List()

	// A directory where delivery.md belongs makes the write fail.
	if err := os.Mkdir(filepath.Join(dir, "delivery.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := finishGoal(st, obj, tasks, dir); code == exitOK {
		t.Fatal("completion was reported even though the delivery report failed")
	}
	after, _, _ := st.GetObjective("obj-1")
	if after.Status == goal.StatusCompleted {
		t.Fatal("objective was marked completed before the delivery report was written")
	}
}

// Cancelling an idle objective must act immediately and leave no flag behind.
func TestCancelIdleLeavesNoFlag(t *testing.T) {
	dir := t.TempDir()
	seedActiveGoal(t, dir)
	if code := goalCancel([]string{"--dir", dir, "--reason", "test"}); code != exitOK {
		t.Fatalf("cancel = %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "cancel")); !os.IsNotExist(err) {
		t.Fatal("cancel flag was left behind with no scheduler")
	}
	st, err := store.OpenReadOnly(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	obj, _, _ := st.GetObjective("obj-1")
	if obj.Status != goal.StatusCancelled {
		t.Fatalf("objective = %s, want cancelled", obj.Status)
	}
}
