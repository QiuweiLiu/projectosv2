// Package replan bounds how a failed plan may be replaced. It never touches
// accepted work and never lets a model rewrite the past.
package replan

import (
	"fmt"
	"sort"
	"strings"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
	"projectos/kernel/internal/verify"
)

const (
	MaxNewTasks = 5
	MaxReplans  = 1
)

// FailureContext describes what blocked, plus what already passed, so the
// planner can add fix tasks without touching accepted ones.
func FailureContext(st *store.Store, old goal.Plan) (string, []domain.TaskID, []string, error) {
	tasks, err := st.List()
	if err != nil {
		return "", nil, nil, err
	}
	oldIDs := map[domain.TaskID]bool{}
	for _, t := range old.Tasks {
		oldIDs[domain.TaskID(t.ID)] = true
	}
	accepted := map[domain.TaskID]bool{}
	var failed []domain.TaskID
	var lines []string
	for _, t := range tasks {
		// A store can hold tasks from several plan versions. Only failures of
		// the plan being replaced belong in the feedback.
		if !oldIDs[t.ID] {
			continue
		}
		switch t.Status {
		case domain.TaskAccepted:
			accepted[t.ID] = true
		case domain.TaskBlocked:
			failed = append(failed, t.ID)
			lines = append(lines, fmt.Sprintf("- %s failed: %s", t.ID, t.LastError))
		}
	}
	var acceptedIDs []string
	for _, t := range old.Tasks {
		if accepted[domain.TaskID(t.ID)] {
			acceptedIDs = append(acceptedIDs, t.ID)
		}
	}
	sort.Strings(acceptedIDs)
	if len(lines) == 0 {
		return "", failed, acceptedIDs, nil
	}
	context := "The following tasks failed and must be fixed by adding new tasks:\n" +
		strings.Join(lines, "\n") +
		"\nKeep every already accepted task exactly as it is: " + strings.Join(acceptedIDs, ", ") +
		fmt.Sprintf("\nAdd at most %d new tasks; do not rename or modify accepted tasks.", MaxNewTasks)
	return context, failed, acceptedIDs, nil
}

// ValidateReplacement enforces the bounded-change rules before anything is written.
func ValidateReplacement(old, new goal.Plan, accepted []string) error {
	return ValidateReplacementLimit(old, new, accepted, MaxNewTasks)
}

// ValidateReplacementLimit is ValidateReplacement with an explicit cap on newly
// added tasks. The automatic path always uses MaxNewTasks; an operator-requested
// full replan may legitimately replace more unfinished work at once.
func ValidateReplacementLimit(old, new goal.Plan, accepted []string, maxNew int) error {
	if maxNew <= 0 {
		maxNew = MaxNewTasks
	}
	if err := new.Validate(); err != nil {
		return err
	}
	oldIDs := map[string]bool{}
	for _, t := range old.Tasks {
		oldIDs[t.ID] = true
	}
	added := 0
	for _, t := range new.Tasks {
		if !oldIDs[t.ID] {
			added++
		}
	}
	if added > maxNew {
		return fmt.Errorf("replacement adds %d new tasks; limit is %d", added, maxNew)
	}
	oldByID := map[string]goal.PlanTask{}
	for _, t := range old.Tasks {
		oldByID[t.ID] = t
	}
	newByID := map[string]goal.PlanTask{}
	for _, t := range new.Tasks {
		newByID[t.ID] = t
	}
	acceptedSet := map[string]bool{}
	for _, id := range accepted {
		acceptedSet[id] = true
	}
	for id := range newByID {
		if _, existed := oldByID[id]; existed && !acceptedSet[id] {
			return fmt.Errorf("replacement reuses failed task %s; give the fix a new id", id)
		}
	}
	for _, id := range accepted {
		prev, ok := oldByID[id]
		if !ok {
			return fmt.Errorf("accepted task %s is not in the old plan", id)
		}
		next, ok := newByID[id]
		if !ok {
			return fmt.Errorf("replacement drops accepted task %s", id)
		}
		if !sameExecutionShape(prev, next) {
			return fmt.Errorf("replacement modifies accepted task %s; accepted tasks are immutable", id)
		}
	}
	return nil
}

// sameExecutionShape compares everything that decides what runs: artifact,
// dependencies, validators and attempt limit. Cosmetic prompt fields may differ,
// but Apply copies the accepted task forward verbatim so the plan cannot drift.
// NormalizeAccepted replaces each accepted task with its previous definition, so
// a paraphrasing model cannot make the plan drift from what actually ran.
func NormalizeAccepted(old, next goal.Plan, accepted []string) goal.Plan {
	oldByID := map[string]goal.PlanTask{}
	for _, t := range old.Tasks {
		oldByID[t.ID] = t
	}
	isAccepted := map[string]bool{}
	for _, id := range accepted {
		isAccepted[id] = true
	}
	out := next
	out.Tasks = make([]goal.PlanTask, 0, len(next.Tasks)+len(accepted))
	seen := map[string]bool{}
	for _, t := range next.Tasks {
		if prev, ok := oldByID[t.ID]; ok && isAccepted[t.ID] {
			t = prev
		}
		seen[t.ID] = true
		out.Tasks = append(out.Tasks, t)
	}
	// Accepted work is never dropped by a model that only lists the new tasks;
	// carry it forward explicitly.
	for _, t := range old.Tasks {
		if isAccepted[t.ID] && !seen[t.ID] {
			out.Tasks = append(out.Tasks, t)
		}
	}
	return out
}

func sameExecutionShape(a, b goal.PlanTask) bool {
	if len(a.Artifacts) != len(b.Artifacts) {
		return false
	}
	for i := range a.Artifacts {
		if a.Artifacts[i] != b.Artifacts[i] {
			return false
		}
	}
	da := append([]string(nil), a.Deps...)
	db := append([]string(nil), b.Deps...)
	sort.Strings(da)
	sort.Strings(db)
	if strings.Join(da, "\x00") != strings.Join(db, "\x00") {
		return false
	}
	va := canonicalValidators(a.Validators)
	vb := canonicalValidators(b.Validators)
	return strings.Join(va, "\x00") == strings.Join(vb, "\x00") && a.MaxAttempts == b.MaxAttempts
}

func canonicalValidators(in []verify.Spec) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, v.Type+"\x00"+v.Command+"\x00"+v.Expect)
	}
	sort.Strings(out)
	return out
}

// Apply records the replacement plan, supersedes tasks it drops, then activates
// the new version. It returns how many tasks were retired.
func Apply(st *store.Store, objectiveID string, version int, old, new goal.Plan, hash string) (int, error) {
	tasks, err := st.List()
	if err != nil {
		return 0, err
	}
	// Carry accepted tasks forward verbatim: a model may paraphrase them, and a
	// plan record that disagrees with what actually ran is a lie.
	var acceptedIDs []string
	for _, t := range tasks {
		if t.Status == domain.TaskAccepted {
			acceptedIDs = append(acceptedIDs, string(t.ID))
		}
		if t.Status == domain.TaskRunning {
			return 0, fmt.Errorf("task %s is still running; stop it before replanning", t.ID)
		}
	}
	new = NormalizeAccepted(old, new, acceptedIDs)

	newIDs := map[string]bool{}
	for _, t := range new.Tasks {
		newIDs[t.ID] = true
	}
	var superseded []domain.TaskID
	for _, t := range tasks {
		if t.Status != domain.TaskPending && t.Status != domain.TaskBlocked {
			continue
		}
		if !newIDs[string(t.ID)] {
			superseded = append(superseded, t.ID)
		}
	}
	return st.ApplyReplan(objectiveID, version, new, hash, superseded,
		"superseded by plan v"+fmt.Sprint(version))
}
