package engine

import (
	"context"
	"path/filepath"
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
