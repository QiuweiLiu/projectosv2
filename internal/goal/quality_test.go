package goal_test

import (
	"testing"

	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/verify"
)

func TestRejectUndeclaredImplementationSymbol(t *testing.T) {
	p := goal.Plan{Tasks: []goal.PlanTask{{
		ID: "T1", Goal: "implement a todo app", Artifacts: []string{"app.py"},
		Acceptance: []string{"it works"},
		Validators: []verify.Spec{{Type: "contains", Expect: "def add_task"}},
	}}}
	if err := goal.ValidateImplementationChecks(p); err == nil {
		t.Fatal("undeclared symbol was accepted")
	}
}

func TestAllowDeclaredImplementationSymbol(t *testing.T) {
	p := goal.Plan{Tasks: []goal.PlanTask{{
		ID: "T1", Goal: "provide add_task(title)", Artifacts: []string{"app.py"},
		Acceptance: []string{"add_task returns title"},
		Validators: []verify.Spec{{Type: "contains", Expect: "def add_task"}},
	}}}
	if err := goal.ValidateImplementationChecks(p); err != nil {
		t.Fatal(err)
	}
}

func TestAllowContentChecks(t *testing.T) {
	p := goal.Plan{Tasks: []goal.PlanTask{{
		ID: "T1", Goal: "write greetings", Artifacts: []string{"a.txt"},
		Validators: []verify.Spec{{Type: "contains", Expect: "hello world"}},
	}}}
	if err := goal.ValidateImplementationChecks(p); err != nil {
		t.Fatal(err)
	}
}
