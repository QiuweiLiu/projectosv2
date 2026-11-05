package engine_test

import (
	"context"
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/engine"
	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/store"
)

func TestNegativeBudgetsMeanUnlimited(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"] = "A-OK"
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, fake, 1, validators()).
		WithBudget(engine.Budget{MaxRuns: -1, MaxReviews: -1, MaxTokens: -1}).
		WithRunTimeout(-1)
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 10); err != nil {
		t.Fatalf("negative limits must not disable the engine: %v", err)
	}
	task, _, _ := st.Get("A")
	if task.Status != domain.TaskAccepted {
		t.Fatalf("task = %s", task.Status)
	}
}

func TestUnwiredEngineReportsError(t *testing.T) {
	e := engine.New(nil, nil, 1, nil)
	if _, err := e.Step(context.Background()); err == nil {
		t.Fatal("an unwired engine must report an error, not panic")
	}
	if _, err := e.Abort(context.Background(), "x"); err == nil {
		t.Fatal("Abort on an unwired engine must report an error")
	}
}
