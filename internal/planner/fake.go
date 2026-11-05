package planner

import (
	"context"
	"fmt"

	"projectos/kernel/internal/goal"
)

// Fake returns canned plans in order. It exists for deterministic tests and the
// `--planner fake` CLI path; it is never evidence of real planning quality.
type Fake struct {
	Plans []goal.Plan
	Err   error
	calls int
}

func (f *Fake) Name() string { return "fake" }

func (f *Fake) Plan(context.Context, Request) (Result, error) {
	if f.Err != nil {
		return Result{}, f.Err
	}
	if f.calls >= len(f.Plans) {
		return Result{}, fmt.Errorf("fake planner has no plan left")
	}
	p := f.Plans[f.calls]
	f.calls++
	return Result{Plan: p, Raw: "fake"}, nil
}
