// Package recovery turns a deterministic failure diagnosis into one bounded
// Kernel-approved action. It never writes task state directly; the CLI applies
// the decision through Store.RequeueBlocked or the existing replanner.
package recovery

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/progress"
	"projectos/kernel/internal/store"
)

type Policy struct {
	MaxRecoveries int
	AttemptCap    int
	ReplansLeft   int
}

type Decision struct {
	Action           string   `json:"action"` // retry | replan | approve | stop
	TaskID           string   `json:"taskId,omitempty"`
	Category         string   `json:"category,omitempty"`
	Reason           string   `json:"reason"`
	Recoverable      bool     `json:"recoverable"`
	RequiresApproval bool     `json:"requiresApproval"`
	RecoveryCount    int      `json:"recoveryCount"`
	MaxRecoveries    int      `json:"maxRecoveries"`
	ModelAssisted    bool     `json:"modelAssisted"`
	Evidence         []string `json:"evidence,omitempty"`
}

type Pending struct {
	TaskID    string    `json:"taskId"`
	Category  string    `json:"category"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"createdAt"`
}

const (
	pendingKey    = "recovery_pending"
	countKey      = "recovery_count"
	modelCallsKey = "recovery_model_calls"
)

func Decide(st *store.Store, obj goal.Objective, needsApproval bool, policy Policy) (Decision, error) {
	return DecideWithSuggestion(st, obj, needsApproval, policy, nil)
}

// DecideWithSuggestion lets a bounded model classification refine only the
// ambiguous "unknown" category. The category is validated; action, budget and
// attempt limits still come from Kernel policy.
func DecideWithSuggestion(st *store.Store, obj goal.Objective, needsApproval bool, policy Policy, suggestion *Suggestion) (Decision, error) {
	if policy.AttemptCap <= 0 {
		policy.AttemptCap = 3
	}
	report, err := progress.AssessWithBudget(st, obj, needsApproval, policy.MaxRecoveries)
	if err != nil {
		return Decision{}, err
	}
	d := Decision{RecoveryCount: report.RecoveryCount, MaxRecoveries: policy.MaxRecoveries}
	if report.Failure != nil && suggestion != nil && report.Failure.Category == "unknown" &&
		suggestedCategories[suggestion.Category] {
		applySuggestion(report.Failure, suggestion)
		d.ModelAssisted = true
	}
	if report.Failure == nil {
		d.Action = "stop"
		d.Reason = "no blocking failure to recover"
		return d, nil
	}
	d.TaskID = report.Failure.TaskID
	d.Category = report.Failure.Category
	d.Reason = report.Failure.Category
	d.Evidence = append([]string(nil), report.Failure.Evidence...)
	d.Recoverable = report.Failure.Recoverable
	d.RequiresApproval = report.Failure.RequiresApproval
	// Human approval is not an automatic recovery: it is allowed even after the
	// auto-recovery budget is spent. The budget still bounds retries below.
	if report.Failure.RequiresApproval {
		task, ok, err := st.Get(domain.TaskID(report.Failure.TaskID))
		if err != nil {
			return d, err
		}
		if !ok || task.Status != domain.TaskBlocked && task.Status != domain.TaskRejected {
			d.Action = "stop"
			d.Reason = "task is no longer retryable"
			return d, nil
		}
		if task.Attempts >= policy.AttemptCap {
			d.Action = "stop"
			d.Reason = "attempt cap reached"
			return d, nil
		}
		d.Action = "approve"
		d.Reason = "approval is required before this task can be retried"
		return d, nil
	}
	if policy.MaxRecoveries >= 0 && report.RecoveryCount >= policy.MaxRecoveries {
		d.Action = "stop"
		d.Reason = "recovery budget exhausted"
		return d, nil
	}
	if report.Failure.Recoverable {
		task, ok, err := st.Get(domain.TaskID(report.Failure.TaskID))
		if err != nil {
			return d, err
		}
		if !ok || task.Status != domain.TaskBlocked && task.Status != domain.TaskRejected {
			d.Action = "stop"
			d.Reason = "task is no longer retryable"
			return d, nil
		}
		if task.Attempts >= policy.AttemptCap {
			d.Action = "stop"
			d.Reason = "attempt cap reached"
			return d, nil
		}
		d.Action = "retry"
		return d, nil
	}
	if report.Failure.Category == "dependency_failure" && policy.ReplansLeft > 0 {
		d.Action = "replan"
		return d, nil
	}
	d.Action = "stop"
	if report.Failure.Category == "unknown" || report.Failure.Category == "unknown_execution_state" {
		d.Reason = "outcome is unknown; do not retry a possible side effect"
	}
	return d, nil
}

func applySuggestion(failure *progress.Failure, suggestion *Suggestion) {
	switch suggestion.Category {
	case "artifact_mismatch", "review_blocked", "provider_transient":
		failure.Category = suggestion.Category
		failure.Recoverable = true
		failure.Action = "retry"
	case "dependency_failure":
		failure.Category = suggestion.Category
		failure.Action = "replan"
	case "permission_denied", "resource_exhaustion":
		failure.Category = suggestion.Category
		failure.RequiresApproval = true
		failure.Action = "approve"
	case "environment_failure", "plan_invalid":
		failure.Category = suggestion.Category
		failure.Action = "inspect"
	case "unknown_execution_state", "unexpected_side_effect":
		failure.Category = suggestion.Category
		failure.Action = "inspect"
	}
}

func Count(st *store.Store) (int, error) {
	raw, err := st.GetMeta(countKey)
	if err != nil || raw == "" {
		return 0, err
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("recovery count %q: %w", raw, err)
	}
	return n, nil
}

func Budget(st *store.Store) int {
	raw, err := st.GetMeta("recovery_budget")
	if err != nil || raw == "" {
		return 2
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 2
	}
	return n
}

func ModelCalls(st *store.Store) (int, error) {
	raw, err := st.GetMeta(modelCallsKey)
	if err != nil || raw == "" {
		return 0, err
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("recovery model calls %q: %w", raw, err)
	}
	return n, nil
}

func IncrementModelCalls(st *store.Store) (int, error) {
	n, err := ModelCalls(st)
	if err != nil {
		return 0, err
	}
	n++
	if err := st.SetMeta(modelCallsKey, strconv.Itoa(n)); err != nil {
		return 0, err
	}
	return n, nil
}

func Increment(st *store.Store) (int, error) {
	n, err := Count(st)
	if err != nil {
		return 0, err
	}
	n++
	if err := st.SetMeta(countKey, strconv.Itoa(n)); err != nil {
		return 0, err
	}
	return n, nil
}

func SetPending(st *store.Store, d Decision) error {
	if d.TaskID == "" {
		return fmt.Errorf("recovery pending needs a task id")
	}
	raw, err := json.Marshal(Pending{TaskID: d.TaskID, Category: d.Category,
		Reason: d.Reason, CreatedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	return st.SetMeta(pendingKey, string(raw))
}

func GetPending(st *store.Store) (Pending, bool, error) {
	raw, err := st.GetMeta(pendingKey)
	if err != nil || raw == "" {
		return Pending{}, false, err
	}
	var p Pending
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return Pending{}, false, fmt.Errorf("recovery pending: %w", err)
	}
	return p, true, nil
}

func ClearPending(st *store.Store) error {
	return st.SetMeta(pendingKey, "")
}
