package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

// A replacement that cannot be recorded must not retire anything: a task with
// no plan that owns it is worse than a failed replan.
func TestApplyReplanIsAtomic(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now().UTC()
	if err := st.CreateObjective(goal.Objective{ID: "obj-1", Goal: "g", Status: goal.StatusActive,
		PlanVersion: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "v"}); err != nil {
		t.Fatal(err)
	}
	spec := goal.Plan{Tasks: []goal.PlanTask{{ID: "T2", Goal: "fix", Artifacts: []string{"b.txt"}, Risk: "low"}}}
	// Occupy version 2 so the insert inside the transaction fails.
	if _, err := st.CreatePlan("obj-1", 2, spec, "existing"); err != nil {
		t.Fatal(err)
	}

	if _, err := st.ApplyReplan("obj-1", 2, spec, "dup", []domain.TaskID{"T1"}, "superseded", store.PlanUsage{}); err == nil {
		t.Fatal("duplicate plan version was accepted")
	}
	task, ok, err := st.Get("T1")
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if task.Status != domain.TaskPending {
		t.Fatalf("task was retired by a failed replan: %s", task.Status)
	}
	obj, _, _ := st.GetObjective("obj-1")
	if obj.PlanVersion != 1 {
		t.Fatalf("objective plan version moved to %d", obj.PlanVersion)
	}
}

// A continuation keeps accepted tasks from an earlier plan. They are still
// members of the current plan and must not disappear from its task list.
func TestTasksForPlanIncludesCarriedAcceptedTasks(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now().UTC()
	if err := st.CreateObjective(goal.Objective{ID: "obj-1", Goal: "g", Status: goal.StatusActive,
		PlanVersion: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	v1 := goal.Plan{Tasks: []goal.PlanTask{{ID: "T1", Goal: "seed", Artifacts: []string{"a.txt"}, Risk: "low"}}}
	if _, err := st.CreatePlan("obj-1", 1, v1, "h1"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "v", ArtifactPath: "a.txt", ObjectiveID: "obj-1", PlanVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Claim("T1", "r1", "fake"); !ok {
		t.Fatal("claim")
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunSucceeded, Accepted: true, Result: "x"}); err != nil {
		t.Fatal(err)
	}

	v2 := goal.Plan{Tasks: []goal.PlanTask{
		{ID: "T1", Goal: "seed", Artifacts: []string{"a.txt"}, Risk: "low"},
		{ID: "T2", Goal: "extra", Artifacts: []string{"b.txt"}, Risk: "low", Deps: []string{"T1"}},
	}}
	if _, err := st.ApplyReplan("obj-1", 2, v2, "h2", nil, "v2", store.PlanUsage{}); err != nil {
		t.Fatal(err)
	}
	tasks, err := st.TasksForPlan("obj-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != "T1" {
		t.Fatalf("tasks = %+v, want the accepted T1 to remain a member", tasks)
	}
}

func TestAmendObjectiveOnlyBeforeExecution(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateObjective(goal.Objective{ID: "obj-1", Goal: "old goal", Status: goal.StatusPlanned,
		PlanVersion: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := st.AmendObjective("obj-1", "new title", "new goal", "c", "e", "a"); err != nil {
		t.Fatalf("amend planned objective: %v", err)
	}
	got, _, _ := st.GetObjective("obj-1")
	if got.Goal != "new goal" || got.Title != "new title" || got.Constraints != "c" {
		t.Fatalf("objective = %+v", got)
	}
	if err := st.CreateTask(domain.Task{ID: "T1", Prompt: "p", ArtifactPath: "a.txt", Validator: "T1"}); err != nil {
		t.Fatal(err)
	}
	// The SQL guard only looks at status; the handler checks tasks. Simulate the
	// status changing to active after a run started.
	if _, ok, err := st.Claim("T1", "r1", "fake"); err != nil || !ok {
		t.Fatalf("claim = %v %v", ok, err)
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunSucceeded, Accepted: true, Result: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveObjective("obj-1", goal.StatusActive); err != nil {
		t.Fatal(err)
	}
	if err := st.AmendObjective("obj-1", "t", "changed after run", "", "", ""); err == nil {
		t.Fatal("objective was amended during execution")
	}
}

func TestApplyReplanRecordsPlanUsage(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	if err := st.CreateObjective(goal.Objective{ID: "obj-1", Goal: "g", Status: goal.StatusActive,
		PlanVersion: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	spec := goal.Plan{Tasks: []goal.PlanTask{{ID: "T2", Goal: "fix", Artifacts: []string{"b.txt"}, Risk: "low"}}}
	if _, err := st.ApplyReplan("obj-1", 2, spec, "h2", nil, "v2", store.PlanUsage{
		PromptChars: 40, OutputChars: 20, PromptTokens: 10, OutputTokens: 5, UsageEstimated: true,
	}); err != nil {
		t.Fatal(err)
	}
	m, err := st.Metrics()
	if err != nil {
		t.Fatal(err)
	}
	if m.PlannerPromptTokens != 10 || m.PlannerOutputTokens != 5 {
		t.Fatalf("planner tokens = %d/%d", m.PlannerPromptTokens, m.PlannerOutputTokens)
	}
}
