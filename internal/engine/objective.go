package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	var versions []string
	for _, task := range tasks {
		if task.Status != domain.TaskAccepted {
			continue
		}
		full, err := e.store.AcceptedResult(task.ID)
		if err != nil {
			return false, fmt.Errorf("objective evidence for %s: %w", task.ID, err)
		}
		sum := sha256.Sum256([]byte(full))
		versions = append(versions, string(task.ID)+"\x00"+task.ArtifactPath+"\x00"+hex.EncodeToString(sum[:]))
		summary = append(summary, fmt.Sprintf("%s -> %s (%d bytes)", task.ID, task.ArtifactPath, len(full)))
		// The model gets a bounded view; the cache identity above never truncates.
		if len(results) < 12 {
			visible := full
			if len(visible) > 8<<10 {
				visible = visible[:8<<10] + "\n... (truncated for model input)"
			}
			results[task.ArtifactPath] = visible
		}
	}
	if len(versions) == 0 {
		return false, fmt.Errorf("objective acceptance has no accepted artifacts")
	}
	sort.Strings(summary)
	sort.Strings(versions)

	version := objectiveVersion(obj, plan, versions)
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
		PlanVersion:     obj.PlanVersion,
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

func objectiveVersion(obj goal.Objective, plan goal.Plan, artifactVersions []string) string {
	h := sha256.New()
	write := func(value string) {
		h.Write([]byte(value))
		h.Write([]byte{0})
	}
	write(obj.ID)
	write(obj.Title)
	write(obj.Goal)
	write(obj.Constraints)
	write(obj.ExpectedOutcome)
	write(obj.Acceptance)
	write(fmt.Sprint(obj.PlanVersion))
	// The complete plan is part of the identity: changing ObjectiveAcceptance,
	// task order, validators, deps or risk must not reuse an old judgment.
	if raw, err := json.Marshal(plan); err == nil {
		h.Write(raw)
		h.Write([]byte{0})
	}
	for _, v := range artifactVersions {
		write(v)
	}
	return hex.EncodeToString(h.Sum(nil))
}
