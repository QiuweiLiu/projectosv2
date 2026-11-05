// Package objective defines the JSON schema a user can hand to the CLI, and the
// small set of built-in validators that avoid hardcoding checks in Go.
package objective

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/engine"
	"projectos/kernel/internal/policy"
	"projectos/kernel/internal/verify"
)

type ValidatorSpec = verify.Spec

type TaskSpec struct {
	ID          string        `json:"id"`
	Objective   string        `json:"objective"`
	Prompt      string        `json:"prompt"`
	Artifact    string        `json:"artifact"`
	Deps        []string      `json:"deps"`
	MaxAttempts int           `json:"maxAttempts"`
	Validator   ValidatorSpec `json:"validator"`
}

type Objective struct {
	Name  string     `json:"name"`
	Pack  string     `json:"pack,omitempty"`
	Tasks []TaskSpec `json:"tasks"`
}

// ApplyPack fills per-task defaults from the capability pack. The Kernel itself
// never learns which pack was used.
func (o Objective) ApplyPack(p policy.Pack) Objective {
	if o.Pack == "" {
		o.Pack = p.Name
	}
	for i := range o.Tasks {
		if o.Tasks[i].MaxAttempts <= 0 {
			o.Tasks[i].MaxAttempts = p.MaxAttempts
		}
	}
	return o
}

func Load(path string) (Objective, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Objective{}, err
	}
	return Parse(raw)
}

// Parse compiles the raw bytes so callers can hash exactly what was validated.
func Parse(raw []byte) (Objective, error) {
	var obj Objective
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&obj); err != nil {
		return obj, fmt.Errorf("parse objective: %w", err)
	}
	return obj, obj.Validate()
}

func (o Objective) Validate() error {
	if len(o.Tasks) == 0 {
		return fmt.Errorf("objective has no tasks")
	}
	ids := map[string]bool{}
	for _, t := range o.Tasks {
		if strings.TrimSpace(t.ID) == "" {
			return fmt.Errorf("task without id")
		}
		if ids[t.ID] {
			return fmt.Errorf("duplicate task id %q", t.ID)
		}
		ids[t.ID] = true
		if t.Validator.Type == "" {
			return fmt.Errorf("task %s has no validator", t.ID)
		}
		switch t.Validator.Type {
		case "exact", "contains":
			if t.Validator.Expect == "" {
				return fmt.Errorf("task %s: validator %q needs a non-empty expect", t.ID, t.Validator.Type)
			}
		case "nonempty":
		default:
			return fmt.Errorf("task %s has unknown validator %q", t.ID, t.Validator.Type)
		}
		if err := validateArtifactPath(t.Artifact); err != nil {
			return fmt.Errorf("task %s: %w", t.ID, err)
		}
	}
	for _, t := range o.Tasks {
		for _, dep := range t.Deps {
			if !ids[dep] {
				return fmt.Errorf("task %s depends on unknown task %q", t.ID, dep)
			}
			if dep == t.ID {
				return fmt.Errorf("task %s depends on itself", t.ID)
			}
		}
	}
	if err := detectCycle(o.Tasks); err != nil {
		return err
	}
	return nil
}

func validateArtifactPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("artifact path is required")
	}
	if filepath.IsAbs(path) || strings.HasPrefix(path, "~") {
		return fmt.Errorf("artifact path must be relative to the worktree: %q", path)
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("artifact path must stay inside the worktree: %q", path)
	}
	return nil
}

// detectCycle rejects dependency cycles, which would otherwise leave tasks
// pending until the whole objective times out.
func detectCycle(tasks []TaskSpec) error {
	deps := map[string][]string{}
	for _, t := range tasks {
		deps[t.ID] = t.Deps
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	state := map[string]int{}
	var visit func(string, []string) error
	visit = func(id string, stack []string) error {
		switch state[id] {
		case gray:
			return fmt.Errorf("dependency cycle: %s -> %s", strings.Join(stack, " -> "), id)
		case black:
			return nil
		}
		state[id] = gray
		for _, dep := range deps[id] {
			if err := visit(dep, append(stack, id)); err != nil {
				return err
			}
		}
		state[id] = black
		return nil
	}
	for _, t := range tasks {
		if err := visit(t.ID, nil); err != nil {
			return err
		}
	}
	return nil
}

// Validators compiles the JSON specs into engine validators.
func (o Objective) Validators() map[string]engine.ValidatorFunc {
	out := map[string]engine.ValidatorFunc{}
	for _, t := range o.Tasks {
		spec := t.Validator
		out[t.ID] = func(result string, _ map[domain.TaskID]string) error {
			return spec.Check(result)
		}
	}
	return out
}

// Tasks converts the spec into domain tasks.
func (o Objective) DomainTasks() []domain.Task {
	tasks := make([]domain.Task, 0, len(o.Tasks))
	for _, t := range o.Tasks {
		deps := make([]domain.TaskID, 0, len(t.Deps))
		for _, d := range t.Deps {
			deps = append(deps, domain.TaskID(d))
		}
		maxAttempts := t.MaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = 1
		}
		tasks = append(tasks, domain.Task{
			ID:           domain.TaskID(t.ID),
			Objective:    t.Objective,
			Prompt:       t.Prompt,
			ArtifactPath: t.Artifact,
			Validator:    t.ID,
			Deps:         deps,
			MaxAttempts:  maxAttempts,
		})
	}
	return tasks
}
