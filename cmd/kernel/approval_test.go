package main

import (
	"path/filepath"
	"strings"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
	"projectos/kernel/internal/verify"
)

func TestApprovalReasonsCoverRiskAndCommands(t *testing.T) {
	plan := goal.Plan{Tasks: []goal.PlanTask{
		{ID: "T1", Goal: "normal", Risk: "low", Validators: []verify.Spec{{Type: "nonempty"}}},
		{ID: "T2", Goal: "risky", Risk: "high", Validators: []verify.Spec{{Type: "nonempty"}}},
		{ID: "T3", Goal: "check", Risk: "low", Validators: []verify.Spec{{Type: "command", Command: "python3 -m pytest"}}},
	}}
	reasons := approvalReasons(plan)
	if len(reasons) != 2 {
		t.Fatalf("reasons = %+v", reasons)
	}
	if !strings.Contains(reasons[0], "T2") || !strings.Contains(reasons[1], "T3") {
		t.Fatalf("reasons = %+v", reasons)
	}
}

func TestRunArgsCarryApprovalAndBudget(t *testing.T) {
	c := &controlServer{}
	args := c.runArgs("/tmp/d", runRequest{Reviewer: true, ApproveRisk: true, MaxRuns: 3, MaxReviews: 4, MaxTokens: 500})
	joined := strings.Join(args, " ")
	for _, want := range []string{"--approve-risk", "--reviewer", "--max-runs 3", "--max-reviews 4", "--max-tokens 500"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
}

func TestRunRefusesPlanUntilApproved(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	obj := goal.Objective{ID: "obj-1", Goal: "check", Status: goal.StatusActive, PlanVersion: 1}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
	plan := goal.Plan{Tasks: []goal.PlanTask{{
		ID: "T1", Goal: "check it", Artifacts: []string{"a.txt"}, Risk: "high",
		Validators: []verify.Spec{{Type: "command", Command: "python3 -m pytest"}},
	}}}
	if _, err := st.CreatePlan("obj-1", 1, plan, "hash"); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmPlan("obj-1", 1); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if code := goalRun([]string{"--dir", dir, "--executor", "fake"}); code != exitBlocked {
		t.Fatalf("run = %d, want blocked", code)
	}
	reopened, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	tasks, _ := reopened.List()
	if len(tasks) != 0 {
		t.Fatalf("tasks were created before approval: %+v", tasks)
	}
}

func TestApproveRiskLetsPlanRun(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	obj := goal.Objective{ID: "obj-1", Goal: "risky", Status: goal.StatusActive, PlanVersion: 1}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
	plan := goal.Plan{Tasks: []goal.PlanTask{{
		ID: "T1", Goal: "make it", Artifacts: []string{"a.txt"}, Risk: "high",
		Validators: []verify.Spec{{Type: "nonempty"}},
	}}}
	if _, err := st.CreatePlan("obj-1", 1, plan, "hash"); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmPlan("obj-1", 1); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if code := goalRun([]string{"--dir", dir, "--executor", "fake", "--approve-risk"}); code != exitOK {
		t.Fatalf("run = %d, want ok", code)
	}
	reopened, err := store.OpenReadOnly(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	task, ok, err := reopened.Get("T1")
	if err != nil || !ok || task.Status != domain.TaskAccepted {
		t.Fatalf("task = %+v %v %v", task, ok, err)
	}
}
