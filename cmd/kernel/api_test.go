package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

// seedGoal writes an active objective with one accepted and one waiting task,
// using the same Claim/SettleRun path the engine uses.
func seedGoal(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	obj := goal.Objective{ID: "obj-1", ProjectPath: dir, Goal: "ship it", Status: goal.StatusActive,
		PlanVersion: 1, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
	plan := goal.Plan{ObjectiveAcceptance: []string{"a.txt"}, Tasks: []goal.PlanTask{
		{ID: "T1", Goal: "write a", Artifacts: []string{"a.txt"}},
		{ID: "T2", Goal: "write b", Artifacts: []string{"b.txt"}, Deps: []string{"T1"}},
	}}
	if _, err := st.CreatePlan(obj.ID, 1, plan, "hash-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmPlan(obj.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(domain.Task{ID: "T1", ObjectiveID: obj.ID, PlanVersion: 1,
		ArtifactPath: "a.txt", Validator: "nonempty"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(domain.Task{ID: "T2", ObjectiveID: obj.ID, PlanVersion: 1,
		Deps: []domain.TaskID{"T1"}, Validator: "nonempty"}); err != nil {
		t.Fatal(err)
	}
	run, ok, err := st.Claim("T1", "run-1", "fake")
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if _, err := st.SettleRun(store.Settlement{RunID: run.ID, RunStatus: domain.RunSucceeded,
		Result: "a", Accepted: true, PromptChars: 1, OutputChars: 1}); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestCountTasks(t *testing.T) {
	tasks := []domain.Task{
		{ID: "A", Status: domain.TaskAccepted},
		{ID: "B", Status: domain.TaskBlocked},
		{ID: "C", Status: domain.TaskSuperseded},
		{ID: "D", Status: domain.TaskPending},
	}
	got := countTasks(tasks)
	want := taskCounts{Total: 4, Accepted: 1, Blocked: 1, Superseded: 1, Pending: 1}
	if got != want {
		t.Fatalf("counts = %+v, want %+v", got, want)
	}
}

func TestSplitObjectivePath(t *testing.T) {
	cases := []struct {
		path, id, action string
		ok               bool
	}{
		{"/api/objectives/obj-1", "obj-1", "", true},
		{"/api/objectives/obj-1/cancel", "obj-1", "cancel", true},
		{"/api/objectives/obj-1/a/b", "", "", false},
		{"/api/objectives/", "", "", false},
		{"/api/objectives", "", "", false},
	}
	for _, c := range cases {
		id, action, ok := splitObjectivePath(c.path)
		if id != c.id || action != c.action || ok != c.ok {
			t.Fatalf("%s -> (%q,%q,%v), want (%q,%q,%v)", c.path, id, action, ok, c.id, c.action, c.ok)
		}
	}
}

func TestServeObjectiveListAndDetail(t *testing.T) {
	dir := t.TempDir()
	st := seedGoal(t, dir)
	defer st.Close()

	rec := httptest.NewRecorder()
	serveObjectiveList(rec, newWorkspace(dir))
	var rows []objectiveSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "obj-1" || rows[0].Status != "active" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Tasks.Total != 2 || rows[0].Tasks.Accepted != 1 || rows[0].Tasks.Pending != 1 {
		t.Fatalf("counts = %+v", rows[0].Tasks)
	}

	detailRec := httptest.NewRecorder()
	serveObjectiveDetail(detailRec, st, dir, "obj-1")
	if detailRec.Code != 200 {
		t.Fatalf("detail status = %d", detailRec.Code)
	}
	var detail objectiveDetail
	if err := json.Unmarshal(detailRec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Plan == nil || detail.PlanHash != "hash-1" || len(detail.Tasks) != 2 {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.Delivery || detail.CancelRequested {
		t.Fatalf("detail side state = %+v", detail)
	}

	missingRec := httptest.NewRecorder()
	serveObjectiveDetail(missingRec, st, dir, "nope")
	if missingRec.Code != 404 {
		t.Fatalf("missing objective status = %d", missingRec.Code)
	}
}

func TestCancelIdleMarksObjectiveCancelled(t *testing.T) {
	dir := t.TempDir()
	st := seedGoal(t, dir)
	// A worker is mid-flight on the dependent task when the cancel arrives.
	if _, ok, err := st.Claim("T2", "run-2", "fake"); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	st.Close()

	st2, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	if code := cancelObjective(st2, dir, "test reason"); code != exitOK {
		t.Fatalf("cancelObjective = %d", code)
	}
	st2.Close()

	reopened, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	obj, ok, err := reopened.GetObjective("obj-1")
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if obj.Status != goal.StatusCancelled {
		t.Fatalf("objective status = %s", obj.Status)
	}
	// Accepted work stays accepted: cancelling stops the objective, it does not
	// rewrite what was already verified.
	accepted, _, err := reopened.Get("T1")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != domain.TaskAccepted {
		t.Fatalf("T1 = %s, want accepted", accepted.Status)
	}
	for _, id := range []domain.TaskID{"T2"} {
		task, _, err := reopened.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status != domain.TaskBlocked {
			t.Fatalf("task %s = %s, want blocked", id, task.Status)
		}
	}
}

func TestTakeCancelRequestConsumesFlag(t *testing.T) {
	dir := t.TempDir()
	if _, ok := takeCancelRequest(dir); ok {
		t.Fatal("empty dir must not report a cancel request")
	}
	if err := os.WriteFile(cancelPath(dir), []byte("user closed the window\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reason, ok := takeCancelRequest(dir)
	if !ok || reason != "user closed the window" {
		t.Fatalf("reason=%q ok=%v", reason, ok)
	}
	if cancelRequested(dir) {
		t.Fatal("flag must be consumed exactly once")
	}
}
