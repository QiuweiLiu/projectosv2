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

type capturing struct {
	fake    *exec.FakeBackend
	prompts map[domain.TaskID]string
}

func (c *capturing) Name() string { return "capturing" }
func (c *capturing) Start(ctx context.Context, req exec.StartRequest) (exec.StartResult, error) {
	c.prompts[req.TaskID] = req.Prompt
	return c.fake.Start(ctx, req)
}
func (c *capturing) Observe(ctx context.Context, id string) (exec.State, error) {
	return c.fake.Observe(ctx, id)
}
func (c *capturing) Cancel(ctx context.Context, id string) error { return c.fake.Cancel(ctx, id) }
func (c *capturing) Collect(ctx context.Context, id, _ string) (exec.Artifact, error) {
	return c.fake.Collect(ctx, id, "")
}

func TestDependentPromptCarriesAcceptedArtifact(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	fake := exec.NewFake(1)
	fake.Output["A"] = "42"
	cap := &capturing{fake: fake, prompts: map[domain.TaskID]string{}}
	checks := map[string]engine.ValidatorFunc{
		"any": func(string, map[domain.TaskID]string) error { return nil },
	}
	e := engine.New(st, cap, 2, checks)
	if err := e.Add(domain.Task{ID: "A", Prompt: "produce", Validator: "any"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Add(domain.Task{ID: "C", Prompt: "A is ${A}", Validator: "any", Deps: []domain.TaskID{"A"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 20); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := cap.prompts["C"]; got != "A is 42" {
		t.Fatalf("dependent prompt = %q, want %q", got, "A is 42")
	}
}
