package progress

import (
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
	"projectos/kernel/internal/verify"
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
	if r.FailureClass != "artifact_mismatch" || r.State != StateRecoverableFailure || r.Next != "retry" {
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
	if r.State != StateCompleted || r.Accepted != 1 {
		t.Fatalf("report = %+v", r)
	}
}

func TestAssessDraftNeedsPlanningAndConfirmation(t *testing.T) {
	st, obj := fixture(t, goal.StatusDraft, nil)
	defer st.Close()
	r, err := Assess(st, obj, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != StateNeedsPlanning || r.Next != "plan" {
		t.Fatalf("draft report = %+v", r)
	}
	plan := goal.Plan{ObjectiveAcceptance: []string{"done"}, Tasks: []goal.PlanTask{{
		ID: "T1", Goal: "write a.txt", Artifacts: []string{"a.txt"}, Risk: "low",
		Validators: []verify.Spec{{Type: "nonempty"}},
	}}}
	if _, err := st.CreatePlan(obj.ID, 1, plan, "h1"); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveObjective(obj.ID, goal.StatusPlanned); err != nil {
		t.Fatal(err)
	}
	r, err = Assess(st, obj, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != StateNeedsConfirmation || r.Next != "confirm" {
		t.Fatalf("planned report = %+v", r)
	}
}

func TestAssessUnknownRunNeedsManualIntervention(t *testing.T) {
	st, obj := fixture(t, goal.StatusFailed, []domain.Task{{
		ID: "T1", Validator: "ok", Status: domain.TaskBlocked, LastError: "interrupted: outcome unknown",
	}})
	defer st.Close()
	r, err := Assess(st, obj, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Failure == nil || r.Failure.Category != "unknown_execution_state" || r.Failure.Recoverable {
		t.Fatalf("report = %+v failure=%+v", r, r.Failure)
	}
	if r.State != StateManualIntervention || r.Next != "inspect" {
		t.Fatalf("state = %s next = %s", r.State, r.Next)
	}
}

func TestAssessPermissionAndBudgetRequireApproval(t *testing.T) {
	for _, tc := range []struct {
		note string
		want string
	}{
		{"permission denied by policy", "permission_denied"},
		{"budget: token limit reached", "resource_exhaustion"},
	} {
		st, obj := fixture(t, goal.StatusActive, []domain.Task{{
			ID: "T1", Validator: "ok", Status: domain.TaskBlocked, LastError: tc.note,
		}})
		r, err := Assess(st, obj, false)
		st.Close()
		if err != nil {
			t.Fatal(err)
		}
		if r.Failure == nil || r.Failure.Category != tc.want || !r.Failure.RequiresApproval {
			t.Fatalf("%s: report = %+v failure=%+v", tc.note, r, r.Failure)
		}
		if r.State != StateNeedsApproval || r.Next != "approve" {
			t.Fatalf("%s: state=%s next=%s", tc.note, r.State, r.Next)
		}
	}
}

func TestAssessProviderTransientIsRecoverable(t *testing.T) {
	st, obj := fixture(t, goal.StatusFailed, []domain.Task{{
		ID: "T1", Validator: "ok", Status: domain.TaskBlocked,
		LastError: "acceptance failed with unknown usage: HTTP 429",
	}})
	defer st.Close()
	r, err := Assess(st, obj, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Failure == nil || r.Failure.Category != "provider_transient" || !r.Failure.Recoverable {
		t.Fatalf("report = %+v failure=%+v", r, r.Failure)
	}
	if r.State != StateRecoverableFailure || r.Next != "retry" {
		t.Fatalf("state=%s next=%s", r.State, r.Next)
	}
}

func TestAssessRunning(t *testing.T) {
	st, obj := fixture(t, goal.StatusActive, []domain.Task{{ID: "T1", Validator: "ok", Status: domain.TaskPending}})
	defer st.Close()
	if _, ok, err := st.Claim("T1", "run-1", "fake"); err != nil || !ok {
		t.Fatal(err, ok)
	}
	r, err := Assess(st, obj, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != StateRunning || r.Next != "wait" || r.Running != 1 {
		t.Fatalf("report = %+v", r)
	}
}

func TestAssessRecoveryBudgetExhaustedIsManual(t *testing.T) {
	st, obj := fixture(t, goal.StatusFailed, []domain.Task{{
		ID: "T1", Validator: "ok", Status: domain.TaskBlocked, LastError: `verification: nope`,
	}})
	defer st.Close()
	if err := st.SetMeta("recovery_budget", "1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMeta("recovery_count", "1"); err != nil {
		t.Fatal(err)
	}
	r, err := Assess(st, obj, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != StateManualIntervention || r.RecoveryAvailable || r.Next != "inspect" {
		t.Fatalf("report = %+v", r)
	}
}

func TestOldReviewDoesNotMaskNewVerificationFailure(t *testing.T) {
	st, obj := fixture(t, goal.StatusFailed, []domain.Task{{ID: "T1", Validator: "ok"}})
	defer st.Close()
	if err := st.AddReview(domain.Review{TaskID: "T1", ArtifactVersion: "v1", Kind: domain.ReviewFull,
		Findings: []domain.Finding{{Severity: domain.SeverityP1, Code: "old", Message: "old defect"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.Claim("T1", "r1", "fake"); err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunSucceeded,
		Note: `verification: artifact does not contain "DONE"`, Evidence: domain.Evidence{
			ArtifactPath: "a.txt", ArtifactVersion: "v2"}}); err != nil {
		t.Fatal(err)
	}
	r, err := Assess(st, obj, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Failure == nil || r.Failure.Category != "artifact_mismatch" {
		t.Fatalf("old review masked the new failure: %+v", r.Failure)
	}
}
