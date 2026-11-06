package ao

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/engine"
	"projectos/kernel/internal/exec"
)

// Acceptance answers a different question from the reviewer: the reviewer looks
// for defects, acceptance asks whether the artifact actually does what the task
// asked. It is fail-closed and cannot write anything.
type Acceptance struct {
	backend exec.Backend
	Timeout time.Duration
	Poll    time.Duration
}

func NewAcceptance(backend exec.Backend) *Acceptance {
	return &Acceptance{backend: backend, Timeout: 5 * time.Minute, Poll: 2 * time.Second}
}

func (a *Acceptance) Review(ctx context.Context, req exec.ReviewRequest) (engine.ReviewOutcome, error) {
	if a == nil || a.backend == nil {
		return engine.ReviewOutcome{Findings: failClosed("acceptance_not_configured", fmt.Errorf("no backend"))}, nil
	}
	return runSession(ctx, a.backend, req, "acceptance", acceptancePrompt(req), a.Timeout, a.Poll)
}

// JudgeObjective is the goal-level acceptance gate. It uses the same read-only
// session shape as per-task acceptance, but the question and evidence are the
// whole objective.
func (a *Acceptance) JudgeObjective(ctx context.Context, req engine.ObjectiveReviewRequest) (engine.ReviewOutcome, error) {
	if a == nil || a.backend == nil {
		return engine.ReviewOutcome{Findings: failClosed("objective_acceptance_not_configured", fmt.Errorf("no backend"))}, nil
	}
	version := objectiveHash(req)
	task := domain.Task{ID: "objective", Objective: req.Goal, ArtifactPath: "out/"}
	artifact := exec.Artifact{Version: version, Result: objectiveEvidence(req), Files: sortedKeys(req.TaskResults)}
	return runSession(ctx, a.backend, exec.ReviewRequest{
		Task: task, Artifact: artifact, Kind: domain.ReviewObjective,
		RunID: "objective:" + req.ObjectiveID + ":" + version,
	}, "objective", objectivePrompt(req), a.Timeout, a.Poll)
}

func objectiveHash(req engine.ObjectiveReviewRequest) string {
	h := sha256.New()
	h.Write([]byte(req.Goal))
	h.Write([]byte{0})
	h.Write([]byte(req.ExpectedOutcome))
	h.Write([]byte{0})
	for _, line := range req.TaskSummary {
		h.Write([]byte(line))
		h.Write([]byte{0})
	}
	for _, key := range sortedKeys(req.TaskResults) {
		h.Write([]byte(key))
		h.Write([]byte{0})
		h.Write([]byte(req.TaskResults[key]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func objectiveEvidence(req engine.ObjectiveReviewRequest) string {
	var b strings.Builder
	for _, key := range sortedKeys(req.TaskResults) {
		b.WriteString("\n--- " + key + " ---\n")
		b.WriteString(req.TaskResults[key])
		if b.Len() > 24<<10 {
			b.WriteString("\n... (truncated)")
			break
		}
	}
	return b.String()
}

func objectivePrompt(req engine.ObjectiveReviewRequest) string {
	criteria := "(none declared; judge against the original goal, do not invent requirements)"
	if len(req.Acceptance) > 0 {
		criteria = "- " + strings.Join(req.Acceptance, "\n- ")
	}
	tasks := "(none)"
	if len(req.TaskSummary) > 0 {
		tasks = "- " + strings.Join(req.TaskSummary, "\n- ")
	}
	return fmt.Sprintf(`You are the objective acceptance judge. Every task below already passed its own
deterministic checks and per-task acceptance. Your only question is whether the
delivered result fulfils the ORIGINAL objective.

Original objective:
%s

Expected outcome:
%s

Objective-level acceptance criteria:
%s

Accepted tasks:
%s

Delivered files (raw content, bounded):
<<<CONTENT
%s
CONTENT>>>

Return JSON only, no prose and no code fences:
{"findings":[{"severity":"P1","code":"short_slug","message":"..."}]}
Use P0/P1 only when a required outcome is missing or contradicted by the delivered files.
Use P2 for improvements that are not required by the original objective.
If the objective is genuinely fulfilled, return {"findings":[]}.`,
		req.Goal, req.ExpectedOutcome, criteria, tasks, objectiveEvidence(req))
}

func acceptancePrompt(req exec.ReviewRequest) string {
	content := req.Artifact.Result
	if len(content) > 6<<10 {
		content = content[:6<<10] + "\n... (truncated to fit the prompt limit)"
	}
	return fmt.Sprintf(`You are the acceptance judge. A worker produced ONE artifact for a task that has
already passed every deterministic check (file content checks and any command checks).

Task objective:
%s

Expected artifact file: %s
Files this run changed: %s
Dependency files already accepted (inputs, never extras): %s

Artifact content (raw, between markers):
<<<CONTENT
%s
CONTENT>>>

Decide one thing: does this artifact actually fulfil the task as stated?
- Missing a requirement the task asked for is P0 or P1.
- A detail that is merely nicer is P2; do not block on P2.
- Do not invent requirements that are not in the task.
- Do not report code style issues; another reviewer already looked at quality.

Reply with JSON only, no prose and no code fences:
{"findings":[{"severity":"P1","code":"short_slug","message":"..."}]}
If the task is genuinely fulfilled, reply {"findings":[]}.`,
		req.Task.Objective, req.Task.ArtifactPath, strings.Join(req.Artifact.Files, ", "),
		strings.Join(req.Deps, ", "), content)
}

var _ = domain.ReviewAcceptance

var _ engine.ObjectiveJudge = (*Acceptance)(nil)
