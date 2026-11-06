package progress

import (
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

func fixture(t *testing.T, status goal.Status, tasks []domain.Task) (*store.Store, goal.Objective) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	obj := goal.Objective{ID: "obj-1", Goal: "g", Status: status, PlanVersion: 1}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		task.ObjectiveID = obj.ID
		task.PlanVersion = 1
		if err := st.CreateTask(task); err != nil {
			t.Fatal(err)
		}
		if task.Status == domain.TaskAccepted || task.Status == domain.TaskBlocked || task.Status == domain.TaskRejected {
			run, ok, err := st.Claim(task.ID, "run-"+string(task.ID), "fake")
			if err != nil || !ok {
				t.Fatal(err, ok)
			}
			status := domain.RunFailed
			accepted := false
			if task.Status == domain.TaskAccepted {
				status, accepted = domain.RunSucceeded, true
			}
			if _, err := st.SettleRun(store.Settlement{RunID: run.ID, RunStatus: status,
				Accepted: accepted, Note: task.LastError, Result: "done"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return st, obj
}

func TestAssessReadyAndApproval(t *testing.T) {
	st, obj := fixture(t, goal.StatusActive, []domain.Task{{ID: "T1", Validator: "ok", Status: domain.TaskPending}})
	defer st.Close()
	r, err := Assess(st, obj, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "needs_approval" || r.Ready != 1 || r.Next == "" {
		t.Fatalf("report = %+v", r)
	}
}

func TestAssessFailureClassification(t *testing.T) {
	st, obj := fixture(t, goal.StatusFailed, []domain.Task{{
		ID: "T1", Validator: "ok", Status: domain.TaskBlocked,
		LastError: `verification: artifact does not contain "def add_task"`,
	}})
	defer st.Close()
	r, err := Assess(st, obj, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.FailureClass != "artifact_mismatch" || r.State != "failed" {
		t.Fatalf("report = %+v", r)
	}
}

func TestAssessCompleted(t *testing.T) {
	st, obj := fixture(t, goal.StatusCompleted, []domain.Task{{
		ID: "T1", Validator: "ok", Status: domain.TaskAccepted,
	}})
	defer st.Close()
	r, err := Assess(st, obj, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "done" || r.Accepted != 1 {
		t.Fatalf("report = %+v", r)
	}
}
