package engine_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/engine"
	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/store"
)

func validators() map[string]engine.ValidatorFunc {
	return map[string]engine.ValidatorFunc{
		"exact:A": func(result string, _ map[domain.TaskID]string) error {
			if result != "A-OK" {
				return fmt.Errorf("expected A-OK, got %q", result)
			}
			return nil
		},
		"exact:B": func(result string, _ map[domain.TaskID]string) error {
			if result != "B-OK" {
				return fmt.Errorf("expected B-OK, got %q", result)
			}
			return nil
		},
		"sum": func(result string, deps map[domain.TaskID]string) error {
			want := "A-OK+B-OK"
			if result != want {
				return fmt.Errorf("expected %q, got %q", want, result)
			}
			if deps["A"] != "A-OK" || deps["B"] != "B-OK" {
				return fmt.Errorf("deps not accepted yet: %v", deps)
			}
			return nil
		},
	}
}

func newEngine(t *testing.T, backend exec.Backend, workers int) (*engine.Engine, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return engine.New(st, backend, workers, validators()), st
}

func diamond(t *testing.T, e *engine.Engine, maxAttempts int) {
	t.Helper()
	for _, task := range []domain.Task{
		{ID: "A", Prompt: "produce A", Validator: "exact:A", MaxAttempts: maxAttempts},
		{ID: "B", Prompt: "produce B", Validator: "exact:B", MaxAttempts: maxAttempts},
		{ID: "C", Prompt: "combine", Validator: "sum", Deps: []domain.TaskID{"A", "B"}, MaxAttempts: maxAttempts},
	} {
		if err := e.Add(task); err != nil {
			t.Fatalf("add %s: %v", task.ID, err)
		}
	}
}

func TestABRunConcurrentlyBeforeC(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"], fake.Output["B"], fake.Output["C"] = "A-OK", "B-OK", "A-OK+B-OK"
	e, st := newEngine(t, fake, 2)
	diamond(t, e, 1)

	if _, err := e.Run(context.Background(), 50); err != nil {
		t.Fatalf("run: %v", err)
	}

	events, err := st.Events()
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	startedC, accepted := -1, map[domain.TaskID]int{}
	for i, ev := range events {
		if ev.TaskID == "C" && ev.Kind == "started" {
			startedC = i
		}
		if ev.Kind == "accepted" {
			accepted[ev.TaskID] = i
		}
	}
	if startedC < 0 {
		t.Fatal("C never started")
	}
	for _, dep := range []domain.TaskID{"A", "B"} {
		if accepted[dep] == 0 || accepted[dep] > startedC {
			t.Fatalf("C started before %s was accepted (indices: %v, startedC=%d)", dep, accepted, startedC)
		}
	}
	if got := fake.PeakConcurrent(); got < 2 {
		t.Fatalf("expected A and B to overlap, peak concurrency %d", got)
	}
}

func TestFailedVerificationBlocksC(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"], fake.Output["B"] = "WRONG", "B-OK"
	e, st := newEngine(t, fake, 2)
	diamond(t, e, 1)

	if _, err := e.Run(context.Background(), 50); err != nil {
		t.Fatalf("run: %v", err)
	}
	a, _, _ := st.Get("A")
	c, _, _ := st.Get("C")
	if a.Status != domain.TaskBlocked {
		t.Fatalf("A should be blocked after failed verification, got %s", a.Status)
	}
	if c.Status != domain.TaskBlocked {
		t.Fatalf("C must be blocked when its dependency failed; got %s", c.Status)
	}
}

func TestRetryIsBounded(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"], fake.Output["B"] = "WRONG", "B-OK"
	e, st := newEngine(t, fake, 2)
	diamond(t, e, 2)

	if _, err := e.Run(context.Background(), 50); err != nil {
		t.Fatalf("run: %v", err)
	}
	a, _, _ := st.Get("A")
	if a.Attempts != 2 {
		t.Fatalf("expected exactly 2 attempts, got %d", a.Attempts)
	}
	if a.Status != domain.TaskBlocked {
		t.Fatalf("expected blocked after exhausting attempts, got %s", a.Status)
	}
}

func TestClaimIsExclusive(t *testing.T) {
	e, st := newEngine(t, exec.NewFake(1), 1)
	diamond(t, e, 1)
	ready, err := st.Ready()
	if err != nil || len(ready) != 2 {
		t.Fatalf("ready: %v %v", ready, err)
	}
	if _, ok, _ := st.Claim("A", "run-1", "fake"); !ok {
		t.Fatal("first claim should succeed")
	}
	if _, ok, _ := st.Claim("A", "run-2", "fake"); ok {
		t.Fatal("second claim must not succeed")
	}
}

func TestRestartMarksRunningUnknown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kernel.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	e := engine.New(st, exec.NewFake(99), 2, validators())
	diamond(t, e, 1)
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	st.Close()

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	for _, id := range []domain.TaskID{"A", "B"} {
		task, _, _ := reopened.Get(id)
		if task.Status != domain.TaskBlocked {
			t.Fatalf("%s should be blocked after restart, got %s", id, task.Status)
		}
	}
}
