// Package progress turns kernel state into a deterministic next-action report.
// It never asks a model for a decision and never mutates task state.
package progress

import (
	"fmt"
	"strings"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

type Report struct {
	ObjectiveID     string `json:"objectiveId"`
	Status          string `json:"status"`
	State           string `json:"state"`
	Next            string `json:"next"`
	Ready           int    `json:"ready"`
	Running         int    `json:"running"`
	Pending         int    `json:"pending"`
	Blocked         int    `json:"blocked"`
	Accepted        int    `json:"accepted"`
	NeedsApproval   bool   `json:"needsApproval"`
	FailureClass    string `json:"failureClass,omitempty"`
	SuggestedAction string `json:"suggestedAction,omitempty"`
}

func Assess(st *store.Store, obj goal.Objective, needsApproval bool) (Report, error) {
	tasks, err := st.List()
	if err != nil {
		return Report{}, err
	}
	report := Report{ObjectiveID: obj.ID, Status: string(obj.Status), NeedsApproval: needsApproval}
	var lastError string
	for _, task := range tasks {
		if task.ObjectiveID != obj.ID {
			continue
		}
		switch task.Status {
		case domain.TaskRunning:
			report.Running++
		case domain.TaskPending:
			report.Pending++
		case domain.TaskBlocked, domain.TaskRejected:
			report.Blocked++
		case domain.TaskAccepted:
			report.Accepted++
		}
		if task.LastError != "" {
			lastError = task.LastError
		}
	}
	ready, err := st.Ready()
	if err != nil {
		return Report{}, err
	}
	for _, task := range ready {
		if task.ObjectiveID == obj.ID {
			report.Ready++
		}
	}
	report.FailureClass, report.SuggestedAction = classify(lastError)
	report.State, report.Next = next(obj.Status, report)
	return report, nil
}

func classify(text string) (string, string) {
	text = strings.ToLower(text)
	switch {
	case strings.Contains(text, "verification:"):
		return "artifact_mismatch", "fix the artifact or the validator, then retry"
	case strings.Contains(text, "review:") || strings.Contains(text, "blocking finding"):
		return "review_blocked", "inspect the findings and address the artifact"
	case strings.Contains(text, "budget:"):
		return "budget", "raise the budget or reduce the task scope"
	case strings.Contains(text, "timeout"):
		return "timeout", "inspect the run; increase timeout only with evidence"
	case strings.Contains(text, "collect:") || strings.Contains(text, "start:") || strings.Contains(text, "runtime"):
		return "runtime", "check the executor, provider and network before retrying"
	case strings.Contains(text, "dependency"):
		return "dependency", "inspect the blocked upstream task"
	case strings.Contains(text, "cancel"):
		return "cancelled", "resume or start a new bounded objective"
	default:
		return "", ""
	}
}

func next(status goal.Status, r Report) (string, string) {
	switch status {
	case goal.StatusDraft, goal.StatusPlanned:
		return "needs_confirmation", "confirm the current plan"
	case goal.StatusCompleted:
		return "done", "collect the delivery or continue with a new bounded objective"
	case goal.StatusCancelled:
		return "cancelled", "inspect accepted artifacts; do not rerun them blindly"
	case goal.StatusFailed:
		if r.FailureClass == "review_blocked" {
			return "blocked", "inspect review findings before adding more tasks"
		}
		return "failed", "inspect the failure class before continuing"
	case goal.StatusPaused:
		return "paused", "resume scheduling"
	case goal.StatusActive:
		if r.NeedsApproval {
			return "needs_approval", "review high-risk tasks and command checks"
		}
		if r.Running > 0 {
			return "running", "wait for in-flight tasks; do not resubmit"
		}
		if r.Blocked > 0 && r.Ready == 0 {
			return "blocked", "inspect blocked tasks and their findings"
		}
		if r.Ready > 0 {
			return "ready", "run the scheduler"
		}
		return "waiting", "inspect the current plan and task states"
	default:
		return "unknown", "inspect the objective state"
	}
}

func (r Report) String() string {
	return fmt.Sprintf("objective=%s status=%s state=%s ready=%d running=%d blocked=%d next=%q",
		r.ObjectiveID, r.Status, r.State, r.Ready, r.Running, r.Blocked, r.Next)
}
