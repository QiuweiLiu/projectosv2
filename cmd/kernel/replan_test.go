package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
	"projectos/kernel/internal/verify"
)

func TestGoalReplanReplacesBlockedPlanAndKeepsObjectiveActive(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	obj := goal.Objective{ID: "obj-1", Goal: "make it", Status: goal.StatusActive, PlanVersion: 1}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
	old := goal.Plan{Tasks: []goal.PlanTask{{
		ID: "T1", Goal: "produce a.txt", Artifacts: []string{"a.txt"}, Risk: "low",
		Validators: []verify.Spec{{Type: "nonempty"}},
	}}}
	if _, err := st.CreatePlan("obj-1", 1, old, "hash-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmPlan("obj-1", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(domain.Task{ID: "T1", Prompt: "p", ArtifactPath: "a.txt",
		Validator: "T1", MaxAttempts: 1, ObjectiveID: "obj-1", PlanVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BlockPending("review budget exhausted"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	replacement := goal.Plan{Tasks: []goal.PlanTask{{
		ID: "T1b", Goal: "produce a.txt with the corrected structure", Artifacts: []string{"a.txt"}, Risk: "low",
		Validators: []verify.Spec{{Type: "nonempty"}},
	}}}
	raw, err := json.Marshal(replacement)
	if err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(dir, "replacement.json")
	if err := os.WriteFile(planFile, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	if code := goalReplan([]string{"--dir", dir, "--planner", "fake", "--plan-file", planFile}); code != exitOK {
		t.Fatalf("replan = %d, want ok", code)
	}

	reopened, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, ok, err := reopened.GetObjective("obj-1")
	if err != nil || !ok {
		t.Fatalf("objective = %v %v", ok, err)
	}
	if got.Status != goal.StatusActive || got.PlanVersion != 2 {
		t.Fatalf("objective = %+v", got)
	}
	plan, ok, err := reopened.GetPlan("obj-1", 2)
	if err != nil || !ok || plan.Status != "confirmed" {
		t.Fatalf("plan v2 = %+v %v %v", plan, ok, err)
	}
	oldTask, _, _ := reopened.Get("T1")
	if oldTask.Status != domain.TaskSuperseded {
		t.Fatalf("T1 = %s, want superseded", oldTask.Status)
	}
	newTask, ok, err := reopened.Get("T1b")
	if err != nil || !ok || newTask.Status != domain.TaskPending {
		t.Fatalf("T1b = %+v %v %v", newTask, ok, err)
	}
}

func TestGoalReplanLeavesPlanUntouchedWhenNothingIsBlocked(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	obj := goal.Objective{ID: "obj-1", Goal: "make it", Status: goal.StatusActive, PlanVersion: 1}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
	plan := goal.Plan{Tasks: []goal.PlanTask{{
		ID: "T1", Goal: "produce a.txt", Artifacts: []string{"a.txt"}, Risk: "low",
		Validators: []verify.Spec{{Type: "nonempty"}},
	}}}
	if _, err := st.CreatePlan("obj-1", 1, plan, "hash-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmPlan("obj-1", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(domain.Task{ID: "T1", Prompt: "p", ArtifactPath: "a.txt",
		Validator: "T1", MaxAttempts: 1, ObjectiveID: "obj-1", PlanVersion: 1}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if code := goalReplan([]string{"--dir", dir, "--planner", "fake"}); code != exitOK {
		t.Fatalf("replan = %d, want ok (no-op)", code)
	}
	reopened, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, _, _ := reopened.GetObjective("obj-1")
	if got.PlanVersion != 1 {
		t.Fatalf("plan version changed to %d", got.PlanVersion)
	}
}
