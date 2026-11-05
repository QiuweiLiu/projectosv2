package replan_test

import (
	"context"
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/engine"
	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/replan"
	"projectos/kernel/internal/store"
	"projectos/kernel/internal/verify"
)

func plan(ids ...string) goal.Plan {
	p := goal.Plan{}
	for _, id := range ids {
		p.Tasks = append(p.Tasks, goal.PlanTask{ID: id, Goal: "g", Artifacts: []string{id + ".txt"}, Risk: "low",
			Validators: []verify.Spec{{Type: "exact", Expect: id}}})
	}
	return p
}

func TestValidateReplacementProtectsAcceptedTasks(t *testing.T) {
	old := plan("T1", "T2")
	accepted := []string{"T1"}
	if err := replan.ValidateReplacement(old, plan("T1", "T3"), accepted); err != nil {
		t.Fatalf("adding a task should be allowed: %v", err)
	}
	if err := replan.ValidateReplacement(old, plan("T3"), accepted); err == nil {
		t.Fatal("dropping an accepted task must be rejected")
	}
	modified := plan("T1", "T3")
	modified.Tasks[0].Validators = []verify.Spec{{Type: "exact", Expect: "changed"}}
	if err := replan.ValidateReplacement(old, modified, accepted); err == nil {
		t.Fatal("modifying an accepted task must be rejected")
	}
	tooMany := plan("T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8")
	if err := replan.ValidateReplacement(old, tooMany, accepted); err == nil {
		t.Fatal("too many new tasks must be rejected")
	}
}

func TestApplySupersedesUnfinishedOnly(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateObjective(goal.Objective{ID: "obj", Title: "t", Goal: "g", Status: goal.StatusDraft}); err != nil {
		t.Fatal(err)
	}
	old := plan("T1", "T2")
	for _, task := range old.Tasks {
		if err := st.CreateTask(domain.Task{ID: domain.TaskID(task.ID), Validator: task.ID, ArtifactPath: "x.txt"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, _ := st.Claim("T1", "run-1", "fake"); !ok {
		t.Fatal("claim failed")
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "run-1", RunStatus: domain.RunSucceeded, Accepted: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveObjective("obj", goal.StatusPlanned); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePlan("obj", 1, old, "h1"); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmPlan("obj", 1); err != nil {
		t.Fatal(err)
	}

	next := plan("T1", "T3")
	n, err := replan.Apply(st, "obj", 2, old, next, "h2")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected T2 to be superseded, got %d", n)
	}
	t1, _, _ := st.Get("T1")
	if t1.Status != domain.TaskAccepted {
		t.Fatalf("accepted task changed: %s", t1.Status)
	}
	t2, _, _ := st.Get("T2")
	if t2.Status != domain.TaskSuperseded {
		t.Fatalf("T2 = %s", t2.Status)
	}
	obj, _, _ := st.GetObjective("obj")
	if obj.PlanVersion != 2 || obj.Status != goal.StatusActive {
		t.Fatalf("objective = %+v", obj)
	}
}

func TestSupersededTasksAreTerminalAndNotRunnable(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "T1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SupersedeTasks([]domain.TaskID{"T1"}, "old plan"); err != nil {
		t.Fatal(err)
	}
	fake := exec.NewFake(1)
	e := engine.New(st, fake, 1, nil)
	if _, err := e.Run(context.Background(), 5); err != nil {
		t.Fatalf("superseded-only state must terminate: %v", err)
	}
	task, _, _ := st.Get("T1")
	if task.Status != domain.TaskSuperseded {
		t.Fatalf("T1 = %s", task.Status)
	}
}

func TestNormalizeAcceptedRestoresDroppedAcceptedTasks(t *testing.T) {
	old := goal.Plan{Tasks: []goal.PlanTask{
		{ID: "T1", Goal: "seed", Artifacts: []string{"a.txt"}, Risk: "low"},
		{ID: "T2", Goal: "mid", Artifacts: []string{"b.txt"}, Risk: "low", Deps: []string{"T1"}},
	}}
	// The model only lists the new task and silently paraphrases T1.
	next := goal.Plan{Tasks: []goal.PlanTask{
		{ID: "T1", Goal: "paraphrased", Artifacts: []string{"a.txt"}, Risk: "low"},
		{ID: "T3", Goal: "extra", Artifacts: []string{"c.txt"}, Risk: "low", Deps: []string{"T2"}},
	}}
	out := replan.NormalizeAccepted(old, next, []string{"T1", "T2"})
	if len(out.Tasks) != 3 {
		t.Fatalf("tasks = %d, want 3", len(out.Tasks))
	}
	if out.Tasks[0].Goal != "seed" {
		t.Fatalf("accepted task was paraphrased: %q", out.Tasks[0].Goal)
	}
	if out.Tasks[2].ID != "T2" {
		t.Fatalf("dropped accepted task was not restored: %+v", out.Tasks)
	}
}
