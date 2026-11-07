package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/verify"
)

func openMetricsStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func createDraftObjective(t *testing.T, st *Store) goal.Objective {
	t.Helper()
	obj := goal.Objective{ID: "obj-1", Goal: "test objective", Status: goal.StatusDraft}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestMetricsEmptyAndPlannedObjective(t *testing.T) {
	st := openMetricsStore(t)
	obj := createDraftObjective(t, st)

	m, err := st.Metrics()
	if err != nil {
		t.Fatalf("empty objective metrics: %v", err)
	}
	if m.Tasks != 0 || m.Runs != 0 || m.Reviews != 0 {
		t.Fatalf("empty metrics = %+v", m)
	}
	if m.StartedAt != "" || m.LastRunFinishedAt != "" || m.FinishedAt != "" {
		t.Fatalf("empty timestamps must be empty: %+v", m)
	}
	if m.WallClockMS < 0 {
		t.Fatalf("wall clock = %d", m.WallClockMS)
	}

	plan := goal.Plan{ObjectiveAcceptance: []string{"done"}, Tasks: []goal.PlanTask{{
		ID: "T1", Goal: "write a.txt", Artifacts: []string{"a.txt"}, Risk: "low",
		Validators: []verify.Spec{{Type: "exact", Expect: "done"}},
	}}}
	hash, err := plannerHash(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePlan(obj.ID, 1, plan, hash); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveObjective(obj.ID, goal.StatusPlanned); err != nil {
		t.Fatal(err)
	}
	m, err = st.Metrics()
	if err != nil {
		t.Fatalf("planned objective metrics: %v", err)
	}
	if m.PlanVersions != 1 || m.Runs != 0 || m.FinishedAt != "" {
		t.Fatalf("planned metrics = %+v", m)
	}
}

func TestMetricsRunningThenSettled(t *testing.T) {
	st := openMetricsStore(t)
	createDraftObjective(t, st)
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "ok", ObjectiveID: "obj-1", PlanVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.Claim("T1", "r1", "fake"); err != nil || !ok {
		t.Fatal(err, ok)
	}
	m, err := st.Metrics()
	if err != nil {
		t.Fatal(err)
	}
	if m.Running != 1 || m.ActiveRuns != 1 || m.Runs != 1 {
		t.Fatalf("running metrics = %+v", m)
	}
	if m.StartedAt == "" || m.FinishedAt != "" {
		t.Fatalf("running timestamps = %+v", m)
	}
	if _, err := st.SettleRun(Settlement{RunID: "r1", RunStatus: domain.RunSucceeded, Accepted: true, Result: "done"}); err != nil {
		t.Fatal(err)
	}
	m, err = st.Metrics()
	if err != nil {
		t.Fatal(err)
	}
	if m.Accepted != 1 || m.FirstPass != 1 || m.ActiveRuns != 0 {
		t.Fatalf("settled metrics = %+v", m)
	}
	if m.FinishedAt != "" {
		t.Fatalf("an active objective must not have an objective FinishedAt: %+v", m)
	}
	if m.LastRunFinishedAt == "" {
		t.Fatalf("last run finish should still be visible: %+v", m)
	}
}

// Fixed timestamps make the distinction between summed run time and wall clock
// testable: two overlapping runs can sum to more than the objective's lifetime.
func TestMetricsConcurrentRunsAndWallClock(t *testing.T) {
	st := openMetricsStore(t)
	obj := createDraftObjective(t, st)
	for _, id := range []string{"T1", "T2"} {
		if err := st.CreateTask(domain.Task{ID: domain.TaskID(id), Validator: "ok", ObjectiveID: obj.ID, PlanVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := st.Claim("T1", "r1", "fake"); err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(Settlement{RunID: "r1", RunStatus: domain.RunSucceeded, Accepted: true, Result: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.Claim("T2", "r2", "fake"); err != nil || !ok {
		t.Fatal(err, ok)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`UPDATE runs SET started_at=?, finished_at=? WHERE id='r1'`,
		base.Format(time.RFC3339Nano), base.Add(10*time.Second).Format(time.RFC3339Nano))
	mustExec(`UPDATE runs SET started_at=? WHERE id='r2'`, base.Add(-5*time.Second).Format(time.RFC3339Nano))
	now := time.Now().UTC()
	mustExec(`UPDATE objectives SET created_at=?, updated_at=?, status='active' WHERE id=?`,
		now.Add(-time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), obj.ID)

	m, err := st.Metrics()
	if err != nil {
		t.Fatal(err)
	}
	if m.ActiveRuns != 1 || m.RunDurationMS != 10_000 {
		t.Fatalf("running metrics = %+v", m)
	}
	if m.FinishedAt != "" {
		t.Fatalf("an in-flight run must not produce FinishedAt: %+v", m)
	}
	if m.LastRunFinishedAt == "" {
		t.Fatalf("last finished run should still be visible: %+v", m)
	}
	if m.WallClockMS < int64(59*time.Minute/time.Millisecond) || m.WallClockMS > int64(61*time.Minute/time.Millisecond) {
		t.Fatalf("wall clock = %d, want about one hour", m.WallClockMS)
	}

	if _, err := st.SettleRun(Settlement{RunID: "r2", RunStatus: domain.RunFailed, Result: "bad", Note: "worker failed"}); err != nil {
		t.Fatal(err)
	}
	mustExec(`UPDATE runs SET finished_at=? WHERE id='r2'`, base.Add(5*time.Second).Format(time.RFC3339Nano))
	wallEnd := time.Now().UTC().Add(-time.Minute)
	mustExec(`UPDATE objectives SET created_at=?, updated_at=?, status='completed' WHERE id=?`,
		wallEnd.Add(-time.Hour).Format(time.RFC3339Nano), wallEnd.Format(time.RFC3339Nano), obj.ID)
	m, err = st.Metrics()
	if err != nil {
		t.Fatal(err)
	}
	if m.ActiveRuns != 0 || m.RunDurationMS != 20_000 || m.FailedRuns != 1 {
		t.Fatalf("finished metrics = %+v", m)
	}
	if m.FinishedAt != base.Add(10*time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("FinishedAt = %q", m.FinishedAt)
	}
	if m.WallClockMS != int64(time.Hour/time.Millisecond) {
		t.Fatalf("terminal wall clock = %d", m.WallClockMS)
	}
}

func TestMetricsTokensAreNotDoubleCounted(t *testing.T) {
	st := openMetricsStore(t)
	createDraftObjective(t, st)
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "ok", ObjectiveID: "obj-1", PlanVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.Claim("T1", "r1", "fake"); err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(Settlement{RunID: "r1", RunStatus: domain.RunSucceeded, Accepted: true,
		Result: "done", PromptTokens: 10, OutputTokens: 5}); err != nil {
		t.Fatal(err)
	}
	for _, rev := range []domain.Review{
		{TaskID: "T1", ArtifactVersion: "v1", Kind: domain.ReviewFull, PromptTokens: 20, OutputTokens: 7},
		{TaskID: "T1", ArtifactVersion: "v1", Kind: domain.ReviewAcceptance, PromptTokens: 30, OutputTokens: 9},
		{TaskID: "T1", ArtifactVersion: "v1", Kind: domain.ReviewObjective, PromptTokens: 40, OutputTokens: 11},
	} {
		if rev.Kind == domain.ReviewObjective {
			rev.TaskID = "objective:obj-1"
		}
		if err := st.AddReview(rev); err != nil {
			t.Fatal(err)
		}
	}
	plan := goal.Plan{ObjectiveAcceptance: []string{"done"}, Tasks: []goal.PlanTask{{
		ID: "T1", Goal: "write a.txt", Artifacts: []string{"a.txt"}, Risk: "low",
		Validators: []verify.Spec{{Type: "nonempty"}},
	}}}
	if _, err := st.CreatePlanWithUsage("obj-1", 1, plan, "h1", PlanUsage{
		PromptTokens: 50, OutputTokens: 10, PromptChars: 200, OutputChars: 40}); err != nil {
		t.Fatal(err)
	}
	m, err := st.Metrics()
	if err != nil {
		t.Fatal(err)
	}
	if m.PlannerPromptTokens != 50 || m.PlannerOutputTokens != 10 {
		t.Fatalf("planner tokens = %+v", m)
	}
	if m.WorkerPromptTokens != 10 || m.ReviewerPromptTokens != 20 ||
		m.AcceptancePromptTokens != 30 || m.ObjectivePromptTokens != 40 {
		t.Fatalf("role tokens = %+v", m)
	}
	if m.PromptTokens != 150 || m.OutputTokens != 42 {
		t.Fatalf("total tokens = %+v", m)
	}
	if m.UsageEstimated {
		t.Fatal("real token values must not be marked estimated")
	}
}

func TestMetricsCharFallbackIsMarkedEstimated(t *testing.T) {
	st := openMetricsStore(t)
	createDraftObjective(t, st)
	if err := st.AddReview(domain.Review{TaskID: "T1", ArtifactVersion: "v1", Kind: domain.ReviewFull,
		PromptChars: 400, OutputChars: 80}); err != nil {
		t.Fatal(err)
	}
	m, err := st.Metrics()
	if err != nil {
		t.Fatal(err)
	}
	if m.ReviewerPromptTokens != 100 || m.ReviewerOutputTokens != 20 || !m.UsageEstimated {
		t.Fatalf("estimated usage = %+v", m)
	}
}

// plannerHash avoids importing the planner package's Hash in this internal test.
func plannerHash(p goal.Plan) (string, error) {
	return fmt.Sprintf("hash-%d", len(p.Tasks)), nil
}

func TestRequeueBlockedOnlyTouchesBlockedTasks(t *testing.T) {
	st := openMetricsStore(t)
	createDraftObjective(t, st)
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "ok", ObjectiveID: "obj-1", PlanVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.Claim("T1", "r1", "fake"); err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(Settlement{RunID: "r1", RunStatus: domain.RunFailed, Note: "verification: nope"}); err != nil {
		t.Fatal(err)
	}
	ok, err := st.RequeueBlocked("T1", "recovery retry", 3)
	if err != nil || !ok {
		t.Fatalf("requeue = %v %v", ok, err)
	}
	task, _, _ := st.Get("T1")
	if task.Status != domain.TaskPending || task.MaxAttempts != 2 {
		t.Fatalf("task = %+v", task)
	}
	if !strings.Contains(task.LastError, "verification") {
		t.Fatalf("recovery must preserve the actionable error: %q", task.LastError)
	}
	if ok, _ := st.RequeueBlocked("T1", "again", 3); ok {
		t.Fatal("pending task must not be requeued twice")
	}
}

func TestRequeueBlockedReopensDependencyBlock(t *testing.T) {
	st := openMetricsStore(t)
	createDraftObjective(t, st)
	for _, task := range []domain.Task{
		{ID: "T1", Validator: "ok", ObjectiveID: "obj-1", PlanVersion: 1},
		{ID: "T2", Validator: "ok", ObjectiveID: "obj-1", PlanVersion: 1, Deps: []domain.TaskID{"T1"}},
	} {
		if err := st.CreateTask(task); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, _ := st.Claim("T1", "r1", "fake"); !ok {
		t.Fatal("claim")
	}
	if _, err := st.SettleRun(Settlement{RunID: "r1", RunStatus: domain.RunFailed,
		Note: "verification: nope", AllowRetry: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BlockDependents("dependency blocked"); err != nil {
		t.Fatal(err)
	}
	if task, _, _ := st.Get("T2"); task.Status != domain.TaskBlocked {
		t.Fatalf("T2 = %s, want blocked", task.Status)
	}
	if ok, err := st.RequeueBlocked("T1", "recovery", 3); err != nil || !ok {
		t.Fatalf("requeue = %v %v", ok, err)
	}
	if task, _, _ := st.Get("T2"); task.Status != domain.TaskPending {
		t.Fatalf("dependent was not reopened: %s", task.Status)
	}
}

func TestMetricsCountsRecoveryEvents(t *testing.T) {
	st := openMetricsStore(t)
	createDraftObjective(t, st)
	if err := st.AppendEvent("T1", "recovery_retry", "auto"); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendEvent("T1", "recovery_approved", "manual"); err != nil {
		t.Fatal(err)
	}
	m, err := st.Metrics()
	if err != nil {
		t.Fatal(err)
	}
	if m.AutoRecoveries != 1 || m.ManualRecoveries != 1 {
		t.Fatalf("recoveries = %+v", m)
	}
}

func TestRequeueBlockedHonorsExistingAttemptAllowance(t *testing.T) {
	st := openMetricsStore(t)
	createDraftObjective(t, st)
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "ok", ObjectiveID: "obj-1",
		PlanVersion: 1, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.Claim("T1", "r1", "fake"); err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(Settlement{RunID: "r1", RunStatus: domain.RunSucceeded,
		Note: "review: blocking finding", AllowRetry: false}); err != nil {
		t.Fatal(err)
	}
	ok, err := st.RequeueBlocked("T1", "recovery", 3)
	if err != nil || !ok {
		t.Fatalf("requeue = %v %v", ok, err)
	}
	task, _, _ := st.Get("T1")
	if task.Status != domain.TaskPending || task.MaxAttempts != 3 {
		t.Fatalf("task = %+v", task)
	}
}

func TestConcurrentRequeueOnlyOneWins(t *testing.T) {
	st := openMetricsStore(t)
	createDraftObjective(t, st)
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "ok", ObjectiveID: "obj-1", PlanVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Claim("T1", "r1", "fake"); !ok {
		t.Fatal("claim")
	}
	if _, err := st.SettleRun(Settlement{RunID: "r1", RunStatus: domain.RunFailed, Note: "verification: nope"}); err != nil {
		t.Fatal(err)
	}
	results := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		go func() {
			ok, _ := st.RequeueBlocked("T1", "recovery", 3)
			results <- ok
		}()
	}
	wins := 0
	for i := 0; i < 8; i++ {
		if <-results {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("requeue winners = %d, want 1", wins)
	}
}
