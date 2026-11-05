package planner

import (
	"context"
	"testing"

	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/verify"
)

func validPlan() goal.Plan {
	return goal.Plan{
		ObjectiveAcceptance: []string{"a.txt exists"},
		Tasks: []goal.PlanTask{{
			ID: "T1", Goal: "produce 42", Artifacts: []string{"a.txt"}, Risk: "low",
			Acceptance: []string{"content is 42"},
			Validators: []verify.Spec{{Type: "exact", Expect: "42"}},
		}},
	}
}

func TestGenerateRetriesAfterInvalidPlan(t *testing.T) {
	bad := goal.Plan{Tasks: []goal.PlanTask{{ID: "T1", Goal: "x", Artifacts: []string{"a.txt"}, Risk: "low"}}} // no validator
	fake := &Fake{Plans: []goal.Plan{bad, validPlan()}}
	req := Request{Goal: "produce 42"}
	plan, _, err := Generate(context.Background(), fake, req, 2)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(plan.Tasks) != 1 || plan.Tasks[0].ID != "T1" {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestGenerateFailsAfterBudget(t *testing.T) {
	bad := goal.Plan{Tasks: []goal.PlanTask{{ID: "T1", Goal: "x", Artifacts: []string{"a.txt"}, Risk: "low"}}}
	fake := &Fake{Plans: []goal.Plan{bad, bad, bad}}
	if _, _, err := Generate(context.Background(), fake, Request{Goal: "g"}, 2); err == nil {
		t.Fatal("invalid plans must exhaust the retry budget")
	}
	if fake.calls != 2 {
		t.Fatalf("expected 2 attempts, got %d", fake.calls)
	}
}

func TestParseRejectsProseAndUnknownFields(t *testing.T) {
	if _, err := Parse("I will do it later"); err == nil {
		t.Fatal("prose must not parse")
	}
	if _, err := Parse(`{"tasks":[],"extra":1}`); err == nil {
		t.Fatal("unknown fields must be rejected")
	}
}

func TestPlanValidation(t *testing.T) {
	cases := map[string]goal.Plan{
		"empty":        {},
		"duplicate":    {Tasks: []goal.PlanTask{{ID: "A", Goal: "g", Artifacts: []string{"a"}, Risk: "low", Validators: []verify.Spec{{Type: "nonempty"}}}, {ID: "A", Goal: "g", Artifacts: []string{"a"}, Risk: "low", Validators: []verify.Spec{{Type: "nonempty"}}}}},
		"cycle":        {Tasks: []goal.PlanTask{{ID: "A", Goal: "g", Deps: []string{"B"}, Artifacts: []string{"a"}, Risk: "low", Validators: []verify.Spec{{Type: "nonempty"}}}, {ID: "B", Goal: "g", Deps: []string{"A"}, Artifacts: []string{"b"}, Risk: "low", Validators: []verify.Spec{{Type: "nonempty"}}}}},
		"unknownDep":   {Tasks: []goal.PlanTask{{ID: "A", Goal: "g", Deps: []string{"Z"}, Artifacts: []string{"a"}, Risk: "low", Validators: []verify.Spec{{Type: "nonempty"}}}}},
		"noArtifact":   {Tasks: []goal.PlanTask{{ID: "A", Goal: "g", Risk: "low", Validators: []verify.Spec{{Type: "nonempty"}}}}},
		"badRisk":      {Tasks: []goal.PlanTask{{ID: "A", Goal: "g", Artifacts: []string{"a"}, Risk: "extreme", Validators: []verify.Spec{{Type: "nonempty"}}}}},
		"escapePath":   {Tasks: []goal.PlanTask{{ID: "A", Goal: "g", Artifacts: []string{"../x"}, Risk: "low", Validators: []verify.Spec{{Type: "nonempty"}}}}},
		"twoArtifacts": {Tasks: []goal.PlanTask{{ID: "A", Goal: "g", Artifacts: []string{"a", "b"}, Risk: "low", Validators: []verify.Spec{{Type: "nonempty"}}}}},
	}
	for name, plan := range cases {
		if err := plan.Validate(); err == nil {
			t.Fatalf("%s: expected a validation error", name)
		}
	}
	if err := validPlan().Validate(); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
}

// The planner is a read-only role: its session must not auto-approve tools.
func TestPlannerSessionIsReadOnly(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["planner"] = `{"objectiveAcceptance":["x"],"tasks":[]}`
	m := NewModel(fake)
	_, _ = m.Plan(context.Background(), Request{Goal: "plan something"})
	if len(fake.Starts) == 0 {
		t.Fatal("planner never started a session")
	}
	if fake.Starts[0].ApprovalMode != "default" {
		t.Fatalf("approval mode = %q, want default", fake.Starts[0].ApprovalMode)
	}
}
