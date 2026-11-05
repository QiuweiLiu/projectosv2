package goal

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"projectos/kernel/internal/verify"
)

const (
	MaxTasks       = 20
	MaxDepth       = 6
	MaxAttemptsCap = 3
)

// PlanTask is the machine-checkable form of one planned task.
type PlanTask struct {
	ID          string        `json:"id"`
	Goal        string        `json:"goal"`
	Scope       string        `json:"scope,omitempty"`
	Inputs      []string      `json:"inputs,omitempty"`
	Deps        []string      `json:"deps,omitempty"`
	Acceptance  []string      `json:"acceptance"`
	Validators  []verify.Spec `json:"validators"`
	Artifacts   []string      `json:"artifacts"`
	Risk        string        `json:"risk"`
	Priority    int           `json:"priority,omitempty"`
	MaxAttempts int           `json:"maxAttempts,omitempty"`
	Allowed     []string      `json:"allowed,omitempty"`
	Forbidden   []string      `json:"forbidden,omitempty"`
}

// Plan is a versioned proposal. It has no authority until the Kernel validates
// it and a human confirms it.
type Plan struct {
	ObjectiveAcceptance []string   `json:"objectiveAcceptance"`
	Tasks               []PlanTask `json:"tasks"`
}

func (p Plan) Validate() error {
	if len(p.Tasks) == 0 {
		return fmt.Errorf("plan has no tasks")
	}
	if len(p.Tasks) > MaxTasks {
		return fmt.Errorf("plan has %d tasks; limit is %d", len(p.Tasks), MaxTasks)
	}
	ids := map[string]bool{}
	artifacts := map[string]string{}
	for _, t := range p.Tasks {
		id := strings.TrimSpace(t.ID)
		if id == "" {
			return fmt.Errorf("task without id")
		}
		if ids[id] {
			return fmt.Errorf("duplicate task id %q", id)
		}
		ids[id] = true
	}
	for _, t := range p.Tasks {
		if strings.TrimSpace(t.Goal) == "" {
			return fmt.Errorf("task %s has no goal", t.ID)
		}
		if len(t.Validators) == 0 {
			return fmt.Errorf("task %s has no machine-checkable validator", t.ID)
		}
		for _, v := range t.Validators {
			if err := v.Validate(); err != nil {
				return fmt.Errorf("task %s: %w", t.ID, err)
			}
		}
		if len(t.Artifacts) == 0 {
			return fmt.Errorf("task %s has no expected artifact", t.ID)
		}
		if len(t.Artifacts) > 1 {
			return fmt.Errorf("task %s declares %d artifacts; one task produces exactly one artifact — split it into separate tasks",
				t.ID, len(t.Artifacts))
		}
		if err := checkArtifactPath(t.Artifacts[0]); err != nil {
			return fmt.Errorf("task %s: %w", t.ID, err)
		}
		clean := filepath.Clean(t.Artifacts[0])
		if other, taken := artifacts[clean]; taken {
			return fmt.Errorf("tasks %s and %s both produce %q; each artifact needs one owner",
				other, t.ID, clean)
		}
		artifacts[clean] = t.ID
		switch t.Risk {
		case "low", "medium", "high":
		default:
			return fmt.Errorf("task %s has invalid risk %q", t.ID, t.Risk)
		}
		if t.MaxAttempts < 0 || t.MaxAttempts > MaxAttemptsCap {
			return fmt.Errorf("task %s maxAttempts must be within 1..%d", t.ID, MaxAttemptsCap)
		}
		for _, dep := range t.Deps {
			if dep == t.ID {
				return fmt.Errorf("task %s depends on itself", t.ID)
			}
			if !ids[dep] {
				return fmt.Errorf("task %s depends on unknown task %q", t.ID, dep)
			}
		}
	}
	if err := detectCycle(p.Tasks); err != nil {
		return err
	}
	if depth(p.Tasks) > MaxDepth {
		return fmt.Errorf("plan depth exceeds %d", MaxDepth)
	}
	return nil
}

func checkArtifactPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("artifact path is empty")
	}
	if filepath.IsAbs(path) || strings.HasPrefix(path, "~") {
		return fmt.Errorf("artifact path must be relative: %q", path)
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == "" {
		return fmt.Errorf("artifact path must name a file: %q", path)
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("artifact path must stay inside the worktree: %q", path)
	}
	return nil
}

func detectCycle(tasks []PlanTask) error {
	deps := map[string][]string{}
	for _, t := range tasks {
		deps[t.ID] = t.Deps
	}
	return cycleDFS(tasks, deps)
}

func cycleDFS(tasks []PlanTask, deps map[string][]string) error {
	state := map[string]int{}
	var visit func(string, []string) error
	visit = func(id string, stack []string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("dependency cycle: %s -> %s", strings.Join(stack, " -> "), id)
		case 2:
			return nil
		}
		state[id] = 1
		for _, dep := range deps[id] {
			if err := visit(dep, append(stack, id)); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for _, t := range tasks {
		if err := visit(t.ID, nil); err != nil {
			return err
		}
	}
	return nil
}

func depth(tasks []PlanTask) int {
	deps := map[string][]string{}
	for _, t := range tasks {
		deps[t.ID] = t.Deps
	}
	memo := map[string]int{}
	var walk func(string) int
	walk = func(id string) int {
		if d, ok := memo[id]; ok {
			return d
		}
		best := 0
		for _, dep := range deps[id] {
			if d := walk(dep) + 1; d > best {
				best = d
			}
		}
		memo[id] = best
		return best
	}
	best := 0
	for _, t := range tasks {
		if d := walk(t.ID); d > best {
			best = d
		}
	}
	return best
}

// SortedTasks returns tasks in a stable order for display and hashing.
func (p Plan) SortedTasks() []PlanTask {
	out := append([]PlanTask(nil), p.Tasks...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
