// Package compile turns a validated plan into Kernel tasks and validators.
// It is the only place that knows both the planner schema and the engine.
package compile

import (
	"fmt"
	"strings"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/engine"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/verify"
)

// Tasks builds the Kernel task set for one plan version.
func Tasks(p goal.Plan, objectiveID string, version int) []domain.Task {
	tasks := make([]domain.Task, 0, len(p.Tasks))
	for _, t := range p.Tasks {
		maxAttempts := t.MaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = 1
		}
		deps := make([]domain.TaskID, 0, len(t.Deps))
		for _, d := range t.Deps {
			deps = append(deps, domain.TaskID(d))
		}
		var commands []verify.Spec
		for _, spec := range t.Validators {
			if spec.Type == "command" {
				commands = append(commands, spec)
			}
		}
		tasks = append(tasks, domain.Task{
			ID:           domain.TaskID(t.ID),
			Objective:    t.Goal,
			Prompt:       prompt(t),
			ArtifactPath: t.Artifacts[0],
			Validator:    t.ID,
			Deps:         deps,
			MaxAttempts:  maxAttempts,
			ObjectiveID:  objectiveID,
			PlanVersion:  version,
			Commands:     commands,
		})
	}
	return tasks
}

// Validators binds each task to the checks the planner declared.
func Validators(p goal.Plan) map[string]engine.ValidatorFunc {
	out := map[string]engine.ValidatorFunc{}
	for _, t := range p.Tasks {
		var specs []verify.Spec
		for _, spec := range t.Validators {
			if spec.Type != "command" {
				specs = append(specs, spec)
			}
		}
		if len(specs) == 0 {
			continue
		}
		out[t.ID] = func(result string, _ map[domain.TaskID]string) error {
			for _, spec := range specs {
				if err := spec.Check(result); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return out
}

// prompt is what the worker actually receives: goal, scope and the exact
// artifact it must leave behind.
func prompt(t goal.PlanTask) string {
	var b strings.Builder
	b.WriteString(t.Goal)
	if t.Scope != "" {
		b.WriteString("\nScope: " + t.Scope)
	}
	if len(t.Inputs) > 0 {
		b.WriteString("\nInputs: " + strings.Join(t.Inputs, ", "))
	}
	b.WriteString("\nRequired artifact: write " + t.Artifacts[0] + " in the current working directory.")
	if len(t.Acceptance) > 0 {
		b.WriteString("\nAcceptance criteria: " + strings.Join(t.Acceptance, "; "))
	}
	// The worker must see the exact machine checks. Otherwise a contains/exact
	// validator can require a symbol the prompt never mentions.
	for _, v := range t.Validators {
		switch v.Type {
		case "exact":
			b.WriteString("\nMachine check: the artifact must equal exactly: " + v.Expect)
		case "contains":
			b.WriteString("\nMachine check: the artifact must contain: " + v.Expect)
		case "nonempty":
			b.WriteString("\nMachine check: the artifact must be non-empty")
		case "command":
			b.WriteString("\nMachine check: this command must exit 0: " + v.Command)
		}
	}
	if len(t.Allowed) > 0 {
		b.WriteString("\nAllowed: " + strings.Join(t.Allowed, "; "))
	}
	if len(t.Forbidden) > 0 {
		b.WriteString("\nForbidden: " + strings.Join(t.Forbidden, "; "))
	}
	b.WriteString("\nDo not create unrelated files. Do not explain unless asked.")
	return b.String()
}

// ValidatePlanForKernel re-checks limits the planner could not bypass.
func ValidatePlanForKernel(p goal.Plan) error {
	if err := p.Validate(); err != nil {
		return err
	}
	for _, t := range p.Tasks {
		if len(t.Validators) == 0 {
			return fmt.Errorf("task %s has no validator", t.ID)
		}
	}
	return nil
}
