package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

type fakeObjectiveJudge struct {
	calls    int
	findings []domain.Finding
	last     ObjectiveReviewRequest
}

func (f *fakeObjectiveJudge) JudgeObjective(_ context.Context, req ObjectiveReviewRequest) (ReviewOutcome, error) {
	f.calls++
	f.last = req
	return ReviewOutcome{Findings: f.findings, PromptTokens: 10, OutputTokens: 5}, nil
}

func acceptedTask(t *testing.T, st *store.Store, objectiveID string) {
	t.Helper()
	if err := st.CreateTask(domain.Task{ID: "T1", ObjectiveID: objectiveID, PlanVersion: 1,
		Prompt: "p", Validator: "ok", ArtifactPath: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	run, ok, err := st.Claim("T1", "r1", "fake")
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(store.Settlement{RunID: run.ID, RunStatus: domain.RunSucceeded,
		Result: "delivered", Accepted: true}); err != nil {
		t.Fatal(err)
	}
}

func objectiveFixture(t *testing.T) (*store.Store, goal.Objective, goal.Plan) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	obj := goal.Objective{ID: "obj-1", Goal: "ship a working app", ExpectedOutcome: "app runs",
		Status: goal.StatusActive, PlanVersion: 1, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
	plan := goal.Plan{ObjectiveAcceptance: []string{"the app starts without errors"},
		Tasks: []goal.PlanTask{{ID: "T1", Goal: "write a.txt", Artifacts: []string{"a.txt"}}}}
	if _, err := st.CreatePlan(obj.ID, 1, plan, "hash"); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmPlan(obj.ID, 1); err != nil {
		t.Fatal(err)
	}
	acceptedTask(t, st, obj.ID)
	return st, obj, plan
}

func TestObjectiveJudgeBlocksCompletion(t *testing.T) {
	st, obj, plan := objectiveFixture(t)
	defer st.Close()
	judge := &fakeObjectiveJudge{findings: []domain.Finding{{
		Severity: domain.SeverityP1, Code: "missing_start", Message: "app does not start",
	}}}
	e := New(st, exec.NewFake(1), 1, nil).WithObjectiveJudge(judge)
	blocked, err := e.JudgeObjective(context.Background(), obj, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Fatal("blocking finding did not block completion")
	}
	if judge.calls != 1 {
		t.Fatalf("judge calls = %d", judge.calls)
	}
	if judge.last.Goal != obj.Goal || len(judge.last.TaskResults) != 1 {
		t.Fatalf("judge request = %+v", judge.last)
	}
}

func TestObjectiveJudgePassesAndCaches(t *testing.T) {
	st, obj, plan := objectiveFixture(t)
	defer st.Close()
	judge := &fakeObjectiveJudge{}
	e := New(st, exec.NewFake(1), 1, nil).WithObjectiveJudge(judge)
	blocked, err := e.JudgeObjective(context.Background(), obj, plan)
	if err != nil || blocked {
		t.Fatalf("first judgment = %v %v", blocked, err)
	}
	blocked, err = e.JudgeObjective(context.Background(), obj, plan)
	if err != nil || blocked {
		t.Fatalf("cached judgment = %v %v", blocked, err)
	}
	if judge.calls != 1 {
		t.Fatalf("judge was called %d times for the same evidence", judge.calls)
	}
}

func TestObjectiveJudgeFailsClosedWhenBudgetExhausted(t *testing.T) {
	st, obj, plan := objectiveFixture(t)
	defer st.Close()
	judge := &fakeObjectiveJudge{}
	e := New(st, exec.NewFake(1), 1, nil).WithObjectiveJudge(judge).WithBudget(Budget{MaxReviews: 1})
	// The task acceptance row does not exist in this fixture; add one so the
	// budget is already spent before the objective judgment.
	if err := st.AddReview(domain.Review{TaskID: "T1", ArtifactVersion: "v1",
		Kind: domain.ReviewAcceptance, Findings: nil}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.JudgeObjective(context.Background(), obj, plan); err == nil {
		t.Fatal("objective judgment passed with no review budget")
	}
}

func addAcceptedArtifact(t *testing.T, st *store.Store, objectiveID, id, path, content string, version int) {
	t.Helper()
	if err := st.CreateTask(domain.Task{ID: domain.TaskID(id), ObjectiveID: objectiveID, PlanVersion: version,
		Prompt: "p", Validator: "ok", ArtifactPath: path}); err != nil {
		t.Fatal(err)
	}
	run, ok, err := st.Claim(domain.TaskID(id), "run-"+id, "fake")
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(store.Settlement{RunID: run.ID, RunStatus: domain.RunSucceeded,
		Result: content, Accepted: true}); err != nil {
		t.Fatal(err)
	}
}

func TestObjectiveJudgeInvalidatesOnAcceptanceChange(t *testing.T) {
	st, obj, plan := objectiveFixture(t)
	defer st.Close()
	judge := &fakeObjectiveJudge{}
	e := New(st, exec.NewFake(1), 1, nil).WithObjectiveJudge(judge)
	if blocked, err := e.JudgeObjective(context.Background(), obj, plan); err != nil || blocked {
		t.Fatal(blocked, err)
	}
	plan.ObjectiveAcceptance = []string{"a different criterion"}
	if _, err := e.JudgeObjective(context.Background(), obj, plan); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 2 {
		t.Fatalf("judge calls = %d, want 2", judge.calls)
	}
}

func TestObjectiveVersionCoversAllArtifacts(t *testing.T) {
	obj, plan := goalObjective()
	base := make([]string, 13)
	for i := range base {
		base[i] = fmt.Sprintf("T%d\x00f%d.txt\x00v%d", i+1, i+1, i+1)
	}
	a := objectiveVersion(obj, plan, base)
	changed := append([]string(nil), base...)
	changed[12] = "T13\x00f13.txt\x00changed-after-cap"
	b := objectiveVersion(obj, plan, changed)
	if a == b {
		t.Fatal("artifact beyond the 12-item input cap did not invalidate the hash")
	}
}

func TestObjectiveVersionCoversFullContent(t *testing.T) {
	obj, plan := goalObjective()
	// The caller hashes the complete content before passing it here; this test
	// documents that the identity is not a prefix of the model-visible text.
	a := objectiveVersion(obj, plan, []string{"long\x00long.txt\x00" + strings.Repeat("a", 64)})
	b := objectiveVersion(obj, plan, []string{"long\x00long.txt\x00" + strings.Repeat("a", 63) + "b"})
	if a == b {
		t.Fatal("tail-only content change did not invalidate the hash")
	}
}

func goalObjective() (goal.Objective, goal.Plan) {
	return goal.Objective{ID: "obj-1", Title: "t", Goal: "ship", ExpectedOutcome: "app runs",
			PlanVersion: 1, Acceptance: "accept"},
		goal.Plan{ObjectiveAcceptance: []string{"the app starts"}}
}

func TestObjectiveJudgePlanVersionInvalidates(t *testing.T) {
	st, obj, plan := objectiveFixture(t)
	defer st.Close()
	judge := &fakeObjectiveJudge{}
	e := New(st, exec.NewFake(1), 1, nil).WithObjectiveJudge(judge)
	if _, err := e.JudgeObjective(context.Background(), obj, plan); err != nil {
		t.Fatal(err)
	}
	plan2 := goal.Plan{ObjectiveAcceptance: plan.ObjectiveAcceptance,
		Tasks: []goal.PlanTask{{ID: "T2", Goal: "second", Artifacts: []string{"b.txt"}}}}
	if _, err := st.CreatePlan(obj.ID, 2, plan2, "hash-2"); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmPlan(obj.ID, 2); err != nil {
		t.Fatal(err)
	}
	obj.PlanVersion = 2
	addAcceptedArtifact(t, st, obj.ID, "T2", "b.txt", "delivered", 2)
	if _, err := e.JudgeObjective(context.Background(), obj, plan); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 2 {
		t.Fatalf("judge calls = %d, want 2", judge.calls)
	}
}

func TestObjectiveJudgeFailsClosedOnClosedStore(t *testing.T) {
	st, obj, plan := objectiveFixture(t)
	judge := &fakeObjectiveJudge{}
	e := New(st, exec.NewFake(1), 1, nil).WithObjectiveJudge(judge)
	st.Close()
	if _, err := e.JudgeObjective(context.Background(), obj, plan); err == nil {
		t.Fatal("closed store did not fail closed")
	}
}
