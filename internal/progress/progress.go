// Package progress turns kernel state into a deterministic next-action report.
// It never asks a model for a decision and never mutates task state.
package progress

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

// State is a deterministic view over the existing lifecycle. It does not add a
// second persisted state machine.
type State string

const (
	StateNeedsPlanning      State = "needs_planning"
	StateNeedsConfirmation  State = "needs_confirmation"
	StateNeedsApproval      State = "needs_approval"
	StateReady              State = "ready"
	StateRunning            State = "running"
	StatePaused             State = "paused"
	StateBlocked            State = "blocked"
	StateRecoverableFailure State = "recoverable_failure"
	StateManualIntervention State = "manual_intervention"
	StateCompleted          State = "completed"
	StateCancelled          State = "cancelled"
	StateUnknown            State = "unknown"
)

type Failure struct {
	Category         string   `json:"category"`
	TaskID           string   `json:"taskId,omitempty"`
	RunID            string   `json:"runId,omitempty"`
	ReviewVersion    string   `json:"reviewVersion,omitempty"`
	Evidence         []string `json:"evidence,omitempty"`
	Recoverable      bool     `json:"recoverable"`
	RequiresApproval bool     `json:"requiresApproval"`
	Action           string   `json:"action"`
}

type Report struct {
	ObjectiveID       string   `json:"objectiveId"`
	Status            string   `json:"status"`
	State             State    `json:"state"`
	Next              string   `json:"next"`
	Ready             int      `json:"ready"`
	Running           int      `json:"running"`
	Pending           int      `json:"pending"`
	Blocked           int      `json:"blocked"`
	Accepted          int      `json:"accepted"`
	NeedsApproval     bool     `json:"needsApproval"`
	Failure           *Failure `json:"failure,omitempty"`
	FailureClass      string   `json:"failureClass,omitempty"`
	SuggestedAction   string   `json:"suggestedAction,omitempty"`
	RecoveryCount     int      `json:"recoveryCount"`
	RecoveryBudget    int      `json:"recoveryBudget"`
	RecoveryAvailable bool     `json:"recoveryAvailable"`
}

func Assess(st *store.Store, obj goal.Objective, needsApproval bool) (Report, error) {
	return AssessWithBudget(st, obj, needsApproval, -1)
}

func AssessWithBudget(st *store.Store, obj goal.Objective, needsApproval bool, maxRecoveries int) (Report, error) {
	if maxRecoveries < 0 {
		maxRecoveries = 2
		if raw, err := st.GetMeta("recovery_budget"); err == nil && raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
				maxRecoveries = n
			}
		}
	}
	tasks, err := st.List()
	if err != nil {
		return Report{}, err
	}
	report := Report{ObjectiveID: obj.ID, Status: string(obj.Status), NeedsApproval: needsApproval,
		RecoveryBudget: maxRecoveries}
	run, hasRun, err := st.LatestPlan(obj.ID)
	if err != nil {
		return Report{}, err
	}
	hasPlan := hasRun && run.Version > 0
	if raw, err := st.GetMeta("recovery_count"); err == nil {
		_, _ = fmt.Sscanf(raw, "%d", &report.RecoveryCount)
	}
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

	failure, err := diagnose(st, obj, tasks)
	if err != nil {
		return Report{}, err
	}
	report.Failure = failure
	if failure != nil {
		report.FailureClass = failure.Category
		report.SuggestedAction = failure.Action
	}
	if failure != nil {
		report.RecoveryAvailable = failure.RequiresApproval || (failure.Recoverable && report.RecoveryCount < maxRecoveries)
		for _, task := range tasks {
			if string(task.ID) == failure.TaskID && task.Attempts >= 3 {
				report.RecoveryAvailable = false
			}
		}
	}
	report.State, report.Next = nextState(obj.Status, hasPlan, report)
	if failure != nil {
		// SuggestedAction must be executable in the current state. A diagnosis
		// may say "retry" while the budget is exhausted; Next says "inspect".
		report.SuggestedAction = report.Next
	}
	return report, nil
}

// diagnose uses structured Run/Review facts first and only falls back to the
// persisted error text when no structured signal exists. Unknown input stays
// unknown; it is never turned into a confident retry.
func diagnose(st *store.Store, obj goal.Objective, tasks []domain.Task) (*Failure, error) {
	var candidates []domain.Task
	for _, task := range tasks {
		if task.ObjectiveID != obj.ID {
			continue
		}
		if task.Status == domain.TaskBlocked || task.Status == domain.TaskRejected {
			candidates = append(candidates, task)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	var best *Failure
	for _, task := range candidates {
		failure, err := diagnoseTask(st, task)
		if err != nil {
			return nil, err
		}
		if failure == nil {
			continue
		}
		if best == nil || severity(failure.Category) > severity(best.Category) {
			best = failure
		}
	}
	return best, nil
}

func diagnoseTask(st *store.Store, task domain.Task) (*Failure, error) {
	runs, err := st.RunsForTask(task.ID)
	if err != nil {
		return nil, err
	}
	var last *domain.Run
	for i := range runs {
		if runs[i].Status != domain.RunRunning {
			last = &runs[i]
		}
	}
	f := &Failure{TaskID: string(task.ID), Evidence: []string{clip(task.LastError, 240)}}
	if last != nil {
		f.RunID = last.ID
		f.Evidence = append(f.Evidence, clip(last.Evidence, 240))
		if last.Status == domain.RunUnknown {
			f.Category = "unknown_execution_state"
			f.Action = "inspect"
			return f, nil
		}
	}
	reviews, err := st.ReviewsFor(task.ID)
	if err != nil {
		return nil, err
	}
	// Only a review of the latest artifact state can explain the current block;
	// an older blocking review must not mask a later verification failure.
	latestVersion := ""
	if evidence, err := st.EvidenceFor(task.ID); err == nil {
		for _, ev := range evidence {
			latestVersion = ev.ArtifactVersion
		}
	}
	for i := len(reviews) - 1; i >= 0; i-- {
		if latestVersion != "" && reviews[i].ArtifactVersion != latestVersion {
			continue
		}
		if reviews[i].Blocking() {
			f.Category = "review_blocked"
			f.ReviewVersion = reviews[i].ArtifactVersion
			for _, finding := range reviews[i].Findings {
				if finding.Severity == domain.SeverityP0 || finding.Severity == domain.SeverityP1 {
					f.Evidence = append(f.Evidence, clip(finding.Code+": "+finding.Message, 240))
				}
			}
			f.Recoverable = true
			f.Action = "retry"
			return f, nil
		}
	}
	text := strings.ToLower(task.LastError)
	if last != nil {
		text += "\n" + strings.ToLower(last.Evidence)
	}
	switch {
	case strings.Contains(text, "permission") || strings.Contains(text, "approval"):
		f.Category = "permission_denied"
		f.RequiresApproval = true
		f.Action = "approve"
	case strings.Contains(text, "budget:") || strings.Contains(text, "usage unknown"):
		f.Category = "resource_exhaustion"
		f.RequiresApproval = true
		f.Action = "approve"
	case strings.Contains(text, "429") || strings.Contains(text, "overloaded") ||
		strings.Contains(text, "rate limit") || strings.Contains(text, "econnreset") ||
		strings.Contains(text, "temporarily unavailable"):
		f.Category = "provider_transient"
		f.Recoverable = true
		f.Action = "retry"
	case strings.Contains(text, "verification:"):
		f.Category = "artifact_mismatch"
		f.Recoverable = true
		f.Action = "retry"
	case strings.Contains(text, "acceptance:") || strings.Contains(text, "review:"):
		f.Category = "review_blocked"
		f.Recoverable = true
		f.Action = "retry"
	case strings.Contains(text, "dependency"):
		f.Category = "dependency_failure"
		f.Action = "replan"
	case strings.Contains(text, "start:") || strings.Contains(text, "prompt:") ||
		strings.Contains(text, "collect:") || strings.Contains(text, "observe error") ||
		strings.Contains(text, "timeout"):
		f.Category = "environment_failure"
		f.Action = "inspect"
	case strings.Contains(text, "interrupted") || strings.Contains(text, "unknown"):
		f.Category = "unknown_execution_state"
		f.Action = "inspect"
	case strings.Contains(text, "cancel"):
		f.Category = "cancelled"
		f.Action = "inspect"
	default:
		f.Category = "unknown"
		f.Action = "inspect"
	}
	return f, nil
}

func severity(category string) int {
	switch category {
	case "unknown_execution_state":
		return 100
	case "permission_denied", "resource_exhaustion":
		return 90
	case "environment_failure":
		return 80
	case "artifact_mismatch", "review_blocked":
		return 70
	case "provider_transient":
		return 60
	case "dependency_failure":
		return 50
	case "cancelled":
		return 40
	default:
		return 10
	}
}

func nextState(status goal.Status, hasPlan bool, r Report) (State, string) {
	switch status {
	case goal.StatusDraft:
		if !hasPlan {
			return StateNeedsPlanning, "plan"
		}
		return StateNeedsConfirmation, "confirm"
	case goal.StatusPlanned:
		return StateNeedsConfirmation, "confirm"
	case goal.StatusCompleted:
		return StateCompleted, "none"
	case goal.StatusCancelled:
		return StateCancelled, "inspect"
	case goal.StatusPaused:
		return StatePaused, "resume"
	case goal.StatusFailed:
		if r.Failure != nil && r.Failure.Recoverable && r.RecoveryAvailable {
			return StateRecoverableFailure, r.Failure.Action
		}
		if r.Failure != nil && r.Failure.RequiresApproval {
			if r.RecoveryAvailable {
				return StateManualIntervention, "approve"
			}
			return StateManualIntervention, "inspect"
		}
		if r.Failure != nil && r.Failure.Category != "unknown" {
			return StateManualIntervention, "inspect"
		}
		return StateUnknown, "inspect"
	case goal.StatusActive:
		if r.NeedsApproval {
			return StateNeedsApproval, "approve"
		}
		if r.Running > 0 {
			return StateRunning, "wait"
		}
		if r.Blocked > 0 {
			if r.Failure != nil && r.Failure.Recoverable && r.RecoveryAvailable {
				return StateRecoverableFailure, r.Failure.Action
			}
			if r.Failure != nil && r.Failure.RequiresApproval {
				if r.RecoveryAvailable {
					return StateNeedsApproval, "approve"
				}
				return StateManualIntervention, "inspect"
			}
			if r.Failure != nil && (r.Failure.Category == "unknown_execution_state" ||
				r.Failure.Category == "environment_failure" || r.Failure.Category == "unknown") {
				return StateManualIntervention, "inspect"
			}
			return StateBlocked, "resolve_blocker"
		}
		if r.Ready > 0 {
			return StateReady, "run"
		}
		if r.Pending > 0 {
			// No READY task while pending work remains means dependencies are not
			// satisfied yet (or form a cycle). "run" would be a false promise.
			return StateBlocked, "resolve_blocker"
		}
		if r.Pending == 0 && r.Accepted > 0 {
			// Every task is accepted; the scheduler still has to run objective
			// acceptance and write delivery. This is a finish step, not a retry.
			return StateReady, "run"
		}
		return StateUnknown, "inspect"
	default:
		return StateUnknown, "inspect"
	}
}

func clip(text string, max int) string {
	text = strings.TrimSpace(text)
	if len(text) <= max {
		return text
	}
	return text[:max] + "…"
}

func (r Report) String() string {
	out := fmt.Sprintf("objective=%s status=%s state=%s ready=%d running=%d blocked=%d next=%q",
		r.ObjectiveID, r.Status, r.State, r.Ready, r.Running, r.Blocked, r.Next)
	if r.Failure != nil {
		out += fmt.Sprintf(" failure=%s task=%s action=%s recoverable=%t approval=%t budget=%d/%d",
			r.Failure.Category, r.Failure.TaskID, r.Failure.Action, r.Failure.Recoverable, r.Failure.RequiresApproval,
			r.RecoveryCount, r.RecoveryBudget)
	}
	return out
}
