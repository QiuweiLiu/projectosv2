package goal_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"projectos/kernel/internal/compile"
	"projectos/kernel/internal/delivery"
	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/engine"
	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/planner"
	"projectos/kernel/internal/store"
	"projectos/kernel/internal/verify"
)

func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func samplePlan() goal.Plan {
	return goal.Plan{
		ObjectiveAcceptance: []string{"a.txt and b.txt exist"},
		Tasks: []goal.PlanTask{
			{ID: "T1", Goal: "write a.txt", Artifacts: []string{"a.txt"}, Risk: "low",
				Validators: []verify.Spec{{Type: "exact", Expect: "alpha"}}},
			{ID: "T2", Goal: "append b.txt", Deps: []string{"T1"}, Artifacts: []string{"b.txt"}, Risk: "low",
				Validators: []verify.Spec{{Type: "exact", Expect: "beta"}}},
		},
	}
}

// Full P1.1 chain: natural-language objective -> planner -> validated plan ->
// human confirm -> Kernel tasks -> execution -> objective completion -> delivery.
func TestObjectiveToDelivery(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)

	obj := goal.Objective{ID: "obj-1", Title: "demo", Goal: "produce alpha and beta", Status: goal.StatusDraft}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}

	plan, _, err := planner.Generate(context.Background(), &planner.Fake{Plans: []goal.Plan{samplePlan()}},
		planner.Request{Goal: obj.Goal}, 2)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := planner.Hash(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePlan(obj.ID, 1, plan, hash); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveObjective(obj.ID, goal.StatusPlanned); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmPlan(obj.ID, 1); err != nil {
		t.Fatal(err)
	}

	fake := exec.NewFake(1)
	for _, task := range plan.Tasks {
		fake.Output[domain.TaskID(task.ID)] = task.Validators[0].Expect
	}
	e := engine.New(st, fake, 2, compile.Validators(plan))
	for _, task := range compile.Tasks(plan, obj.ID, 1) {
		if err := e.Add(task); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Run(context.Background(), 30); err != nil {
		t.Fatalf("engine: %v", err)
	}
	tasks, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.Status != domain.TaskAccepted {
			t.Fatalf("task %s = %s", task.ID, task.Status)
		}
	}
	if err := st.MoveObjective(obj.ID, goal.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	obj.Status = goal.StatusCompleted
	report, err := delivery.Build(st, obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Tasks) != 2 || len(report.Open) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if err := delivery.Write(dir, report); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"delivery.json", "delivery.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
}

func TestPlanVersionConflicts(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	obj := goal.Objective{ID: "obj-1", Title: "t", Goal: "g", Status: goal.StatusDraft}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
	plan := samplePlan()
	if _, err := st.CreatePlan(obj.ID, 1, plan, "h1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePlan(obj.ID, 2, plan, "h2"); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveObjective(obj.ID, goal.StatusPlanned); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmPlan(obj.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmPlan(obj.ID, 1); err == nil {
		t.Fatal("re-confirming the same version must fail")
	}
	if err := st.ConfirmPlan(obj.ID, 2); err != nil {
		t.Fatalf("confirm v2: %v", err)
	}
	old, _, err := st.GetPlan(obj.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != "superseded" {
		t.Fatalf("v1 status = %s", old.Status)
	}
}

func TestObjectiveTransitions(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	obj := goal.Objective{ID: "obj-1", Title: "t", Goal: "g", Status: goal.StatusDraft}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveObjective(obj.ID, goal.StatusCompleted); err == nil {
		t.Fatal("draft -> completed must be refused")
	}
	if err := st.MoveObjective(obj.ID, goal.StatusCancelled); err != nil {
		t.Fatalf("draft -> cancelled should be allowed: %v", err)
	}
	if err := st.MoveObjective(obj.ID, goal.StatusActive); err == nil {
		t.Fatal("cancelled is terminal")
	}
}
