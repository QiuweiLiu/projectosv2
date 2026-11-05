package ao

import (
	"context"
	"fmt"
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
