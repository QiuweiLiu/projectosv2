package compile_test

import (
	"strings"
	"testing"

	"projectos/kernel/internal/compile"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/verify"
)

func plan() goal.Plan {
	return goal.Plan{Tasks: []goal.PlanTask{{
		ID: "T1", Goal: "write the file", Scope: "root only", Artifacts: []string{"a.txt"},
		Risk: "low", Acceptance: []string{"equals 42"},
		Validators: []verify.Spec{{Type: "exact", Expect: "42"}},
		Allowed:    []string{"write files"}, Forbidden: []string{"network"},
	}}}
}

func TestTasksCarryArtifactAndDeps(t *testing.T) {
	tasks := compile.Tasks(plan(), "obj-1", 1)
	if len(tasks) != 1 || tasks[0].ArtifactPath != "a.txt" || tasks[0].Validator != "T1" {
		t.Fatalf("task = %#v", tasks[0])
	}
	if !strings.Contains(tasks[0].Prompt, "a.txt") || !strings.Contains(tasks[0].Prompt, "network") {
		t.Fatalf("prompt = %q", tasks[0].Prompt)
	}
}

func TestValidatorsAreCompiledAndCombined(t *testing.T) {
	p := plan()
	p.Tasks[0].Validators = append(p.Tasks[0].Validators, verify.Spec{Type: "contains", Expect: "4"})
	check := compile.Validators(p)["T1"]
	if check == nil {
		t.Fatal("missing validator")
	}
	if err := check("42", nil); err != nil {
		t.Fatalf("42 should pass: %v", err)
	}
	if err := check("41", nil); err == nil {
		t.Fatal("41 must fail")
	}
}

func TestPromptIncludesMachineChecks(t *testing.T) {
	p := goal.Plan{Tasks: []goal.PlanTask{{
		ID: "T1", Goal: "write a.py", Artifacts: []string{"a.py"},
		Validators: []verify.Spec{{Type: "contains", Expect: "def add_task"}, {Type: "command", Command: "python3 -m unittest"}},
	}}}
	tasks := compile.Tasks(p, "obj-1", 1)
	if len(tasks) != 1 {
		t.Fatal(tasks)
	}
	for _, want := range []string{"must contain: def add_task", "command must exit 0: python3 -m unittest"} {
		if !strings.Contains(tasks[0].Prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, tasks[0].Prompt)
		}
	}
}
