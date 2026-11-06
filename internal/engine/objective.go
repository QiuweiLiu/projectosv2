package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

// JudgeObjective is the final, goal-level gate. Task acceptance is necessary but
// not sufficient: a set of perfectly valid files can still miss the original
// request. The judgment is cached by a content hash so re-running a delivery
// check does not buy a second model opinion on the same evidence.
func (e *Engine) JudgeObjective(ctx context.Context, obj goal.Objective, plan goal.Plan) (bool, error) {
	if e.objectiveJudge == nil {
		return false, nil
	}
	used, err := e.store.CountReviews()
	if err != nil {
		return false, err
	}
	if e.budget.MaxReviews > 0 && used >= e.budget.MaxReviews {
		return false, fmt.Errorf("objective acceptance needs a review but the review budget is exhausted")
	}
	tasks, err := e.store.TasksForPlan(obj.ID, obj.PlanVersion)
	if err != nil {
		return false, err
	}
	results := map[string]string{}
	var summary []string
	for _, task := range tasks {
		if task.Status != domain.TaskAccepted {
			continue
		}
		result, err := e.store.AcceptedResult(task.ID)
		if err != nil {
			return false, err
		}
		if len(result) > 8<<10 {
			result = result[:8<<10] + "\n... (truncated)"
		}
		if len(results) < 12 {
			results[task.ArtifactPath] = result
		}
		summary = append(summary, fmt.Sprintf("%s -> %s (%d bytes)", task.ID, task.ArtifactPath, len(result)))
	}
	sort.Strings(summary)

	version := objectiveVersion(obj, results)
	taskID := domain.TaskID("objective:" + obj.ID)
	if blocking, found, err := e.store.ReviewVerdict(taskID, version, domain.ReviewObjective); err != nil {
		return false, err
	} else if found {
		return blocking, nil
	}

	outcome, jerr := e.objectiveJudge.JudgeObjective(ctx, ObjectiveReviewRequest{
		ObjectiveID: obj.ID, Goal: obj.Goal, ExpectedOutcome: obj.ExpectedOutcome,
		Acceptance: plan.ObjectiveAcceptance, TaskResults: results, TaskSummary: summary,
	})
	findings := normalizeFindings(outcome.Findings)
	if jerr != nil {
		findings = []domain.Finding{{
			Severity: domain.SeverityP1, Code: "objective_acceptance_error", Message: jerr.Error(),
		}}
		if outcome.PromptChars+outcome.OutputChars+outcome.PromptTokens+outcome.OutputTokens == 0 {
			return false, fmt.Errorf("objective acceptance failed with unknown usage: %w", jerr)
		}
	}
	if err := e.store.AddReview(domain.Review{
		TaskID:          taskID,
		ArtifactVersion: version,
		Kind:            domain.ReviewObjective,
		Findings:        findings,
		PromptChars:     outcome.PromptChars,
		OutputChars:     outcome.OutputChars,
		PromptTokens:    outcome.PromptTokens,
		OutputTokens:    outcome.OutputTokens,
		UsageEstimated:  outcome.UsageEstimated,
	}); err != nil {
		if errors.Is(err, store.ErrReviewLimit) {
			blocking, found, verr := e.store.ReviewVerdict(taskID, version, domain.ReviewObjective)
			if verr != nil {
				return false, verr
			}
			if !found {
				return false, fmt.Errorf("objective review was not recorded: %w", err)
			}
			return blocking, nil
		}
		return false, fmt.Errorf("objective review was not recorded: %w", err)
	}
	for _, f := range findings {
		if f.Severity == domain.SeverityP0 || f.Severity == domain.SeverityP1 {
			return true, nil
		}
	}
	return false, nil
}

func objectiveVersion(obj goal.Objective, results map[string]string) string {
	keys := make([]string, 0, len(results))
	for k := range results {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	h.Write([]byte(obj.Goal))
	h.Write([]byte{0})
	h.Write([]byte(obj.ExpectedOutcome))
	h.Write([]byte{0})
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(results[k]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
