package recovery

import (
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

func fixture(t *testing.T, status goal.Status, task domain.Task) (*store.Store, goal.Objective) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	obj := goal.Objective{ID: "obj-1", Goal: "g", Status: status, PlanVersion: 1}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
	task.ObjectiveID = obj.ID
	task.PlanVersion = 1
	if task.Validator == "" {
		task.Validator = "nonempty"
	}
	if err := st.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	return st, obj
}

func blockTask(t *testing.T, st *store.Store, note string) {
	t.Helper()
	if _, ok, err := st.Claim("T1", "r1", "fake"); err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunFailed,
		Note: note, AllowRetry: false}); err != nil {
		t.Fatal(err)
	}
}

func TestDecideRetriesRecoverableVerificationFailure(t *testing.T) {
	st, obj := fixture(t, goal.StatusFailed, domain.Task{ID: "T1"})
	blockTask(t, st, `verification: artifact does not contain "done"`)
	d, err := Decide(st, obj, false, Policy{MaxRecoveries: 2, AttemptCap: 3})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != "retry" || d.TaskID != "T1" || !d.Recoverable {
		t.Fatalf("decision = %+v", d)
	}
}

func TestDecideUnknownRunIsNeverRetried(t *testing.T) {
	st, obj := fixture(t, goal.StatusFailed, domain.Task{ID: "T1"})
	if _, ok, err := st.Claim("T1", "r1", "fake"); err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunUnknown,
		Note: "interrupted: outcome unknown"}); err != nil {
		t.Fatal(err)
	}
	d, err := Decide(st, obj, false, Policy{MaxRecoveries: 3, AttemptCap: 3})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != "stop" || d.Category != "unknown_execution_state" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestDecidePermissionNeedsApproval(t *testing.T) {
	st, obj := fixture(t, goal.StatusActive, domain.Task{ID: "T1"})
	blockTask(t, st, "permission denied by policy")
	d, err := Decide(st, obj, false, Policy{MaxRecoveries: 3, AttemptCap: 3})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != "approve" || !d.RequiresApproval {
		t.Fatalf("decision = %+v", d)
	}
}

func TestDecideBudgetExhaustion(t *testing.T) {
	st, obj := fixture(t, goal.StatusFailed, domain.Task{ID: "T1"})
	blockTask(t, st, "verification: nope")
	if err := st.SetMeta(countKey, "2"); err != nil {
		t.Fatal(err)
	}
	d, err := Decide(st, obj, false, Policy{MaxRecoveries: 2, AttemptCap: 3})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != "stop" || d.Reason != "recovery budget exhausted" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestDecideAttemptCap(t *testing.T) {
	st, obj := fixture(t, goal.StatusFailed, domain.Task{ID: "T1", MaxAttempts: 3})
	for i, run := range []string{"r1", "r2", "r3"} {
		if _, ok, err := st.Claim("T1", run, "fake"); err != nil || !ok {
			t.Fatal(err, ok)
		}
		if _, err := st.SettleRun(store.Settlement{RunID: run, RunStatus: domain.RunFailed,
			Note: "verification: nope", AllowRetry: i < 2}); err != nil {
			t.Fatal(err)
		}
	}
	d, err := Decide(st, obj, false, Policy{MaxRecoveries: 3, AttemptCap: 3})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != "stop" || d.Reason != "attempt cap reached" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestPendingRoundTrip(t *testing.T) {
	st, _ := fixture(t, goal.StatusActive, domain.Task{ID: "T1"})
	if err := SetPending(st, Decision{TaskID: "T1", Category: "permission_denied", Reason: "approve"}); err != nil {
		t.Fatal(err)
	}
	p, ok, err := GetPending(st)
	if err != nil || !ok || p.TaskID != "T1" {
		t.Fatalf("pending = %+v %v %v", p, ok, err)
	}
	if err := ClearPending(st); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := GetPending(st); ok {
		t.Fatal("pending was not cleared")
	}
}
