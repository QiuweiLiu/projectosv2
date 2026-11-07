package ao

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/engine"
	"projectos/kernel/internal/exec"
)

// Reviewer runs a second OpenCode session that reads one artifact and returns
// structured findings. It is fail-closed: if the session fails, times out, or
// the reply cannot be parsed, the artifact is treated as P1.
type Reviewer struct {
	backend exec.Backend
	Timeout time.Duration
	Poll    time.Duration
}

func NewReviewer(backend exec.Backend) *Reviewer {
	return &Reviewer{backend: backend, Timeout: 5 * time.Minute, Poll: 2 * time.Second}
}

func (r *Reviewer) Review(ctx context.Context, req exec.ReviewRequest) (engine.ReviewOutcome, error) {
	if r == nil || r.backend == nil {
		return engine.ReviewOutcome{Findings: failClosed("reviewer_not_configured", fmt.Errorf("no backend"))}, nil
	}
	return runSession(ctx, r.backend, req, "reviewer", reviewPrompt(req), r.Timeout, r.Poll)
}

// runSession retries only known transient provider failures, with bounded
// backoff. A malformed answer, a policy error, or an unknown session is never
// retried; those must stay fail-closed.
func runSession(ctx context.Context, backend exec.Backend, req exec.ReviewRequest, role, prompt string,
	timeout, poll time.Duration) (engine.ReviewOutcome, error) {
	var total engine.ReviewOutcome
	for attempt := 0; attempt < 3; attempt++ {
		out, err := runSessionOnce(ctx, backend, req, role, prompt, timeout, poll, attempt)
		total.PromptChars += out.PromptChars
		total.OutputChars += out.OutputChars
		total.PromptTokens += out.PromptTokens
		total.OutputTokens += out.OutputTokens
		total.UsageEstimated = total.UsageEstimated || out.UsageEstimated
		if err == nil {
			total.Findings = out.Findings
			return total, nil
		}
		if !transientProviderError(err.Error()) || attempt == 2 {
			out.PromptChars, out.OutputChars = total.PromptChars, total.OutputChars
			out.PromptTokens, out.OutputTokens = total.PromptTokens, total.OutputTokens
			out.UsageEstimated = total.UsageEstimated
			return out, err
		}
		select {
		case <-ctx.Done():
			out.PromptChars, out.OutputChars = total.PromptChars, total.OutputChars
			out.PromptTokens, out.OutputTokens = total.PromptTokens, total.OutputTokens
			out.UsageEstimated = total.UsageEstimated
			return out, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * retryBackoff):
		}
	}
	return total, fmt.Errorf("session retries exhausted")
}

// retryBackoff is a variable so tests can exercise the retry path without
// sleeping for real provider backoff.
var retryBackoff = 2 * time.Second

func transientProviderError(text string) bool {
	text = strings.ToLower(text)
	// A session timeout is deliberately not on this list: the model may still be
	// working, and retrying would triple a five-minute wait without new evidence.
	for _, needle := range []string{"429", "overloaded", "rate limit", "rate_limit",
		"econnreset", "transport", "socket", "eof", "temporarily unavailable"} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// runSessionOnce is the shared shape of every model judgment: one session, one
// artifact, JSON findings, fail-closed on anything unexpected.
func runSessionOnce(ctx context.Context, backend exec.Backend, req exec.ReviewRequest, role, prompt string,
	timeout, poll time.Duration, attempt int) (engine.ReviewOutcome, error) {
	// A retry must be a new session. Reusing the idempotency key makes AO replay
	// the failed session instead of asking the provider again.
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s%s-%d", req.RunID, req.Kind, attempt)))
	start, err := backend.Start(ctx, exec.StartRequest{
		TaskID:       domain.TaskID(role + "-" + string(req.Task.ID)),
		Attempt:      1,
		Key:          hex.EncodeToString(sum[:8]),
		ApprovalMode: "default",
		Prompt:       prompt,
		Files:        req.SeedFiles,
	})
	if err != nil {
		return engine.ReviewOutcome{Findings: failClosed(role+"_start", err)}, err
	}
	defer func() {
		_ = backend.Cancel(context.Background(), start.ExternalID)
	}()

	deadline := time.Now().Add(timeout)
	for {
		state, oerr := backend.Observe(ctx, start.ExternalID)
		if oerr != nil {
			return engine.ReviewOutcome{Findings: failClosed(role+"_observe", oerr)}, oerr
		}
		if state.Status != domain.RunRunning {
			if state.Status != domain.RunSucceeded {
				err := fmt.Errorf("session ended as %s: %s", state.Status, strings.TrimSpace(state.Evidence))
				out := engine.ReviewOutcome{Findings: failClosed(role+"_run", err)}
				if collected, cerr := backend.Collect(ctx, start.ExternalID, ""); cerr == nil {
					out.PromptChars, out.OutputChars = collected.PromptChars, collected.OutputChars
					out.PromptTokens, out.OutputTokens = collected.PromptTokens, collected.OutputTokens
					out.UsageEstimated = collected.UsageEstimated
				}
				return out, err
			}
			break
		}
		if time.Now().After(deadline) {
			err := fmt.Errorf("no reply within %s", timeout)
			return engine.ReviewOutcome{Findings: failClosed(role+"_timeout", err)}, err
		}
		time.Sleep(poll)
	}

	collected, cerr := backend.Collect(ctx, start.ExternalID, "")
	if cerr != nil {
		return engine.ReviewOutcome{Findings: failClosed(role+"_collect", cerr)}, cerr
	}
	findings, perr := parseFindings(collected.Result)
	if perr != nil {
		return engine.ReviewOutcome{
			Findings:       failClosed(role+"_output_unparsable", perr),
			PromptChars:    collected.PromptChars,
			OutputChars:    collected.OutputChars,
			PromptTokens:   collected.PromptTokens,
			OutputTokens:   collected.OutputTokens,
			UsageEstimated: collected.UsageEstimated,
		}, perr
	}
	return engine.ReviewOutcome{Findings: findings,
		PromptChars: collected.PromptChars, OutputChars: collected.OutputChars,
		PromptTokens: collected.PromptTokens, OutputTokens: collected.OutputTokens,
		UsageEstimated: collected.UsageEstimated}, nil
}

func failClosed(code string, err error) []domain.Finding {
	return []domain.Finding{{
		Severity: domain.SeverityP1,
		Code:     code,
		Message:  "review could not be completed: " + err.Error(),
	}}
}

func reviewPrompt(req exec.ReviewRequest) string {
	task, artifact := req.Task, req.Artifact
	scope := "This is a full review."
	if req.Kind == domain.ReviewTargeted {
		var prior strings.Builder
		for _, f := range req.Prior {
			fmt.Fprintf(&prior, "- [%s] %s: %s\n", f.Severity, f.Code, f.Message)
		}
		scope = fmt.Sprintf(`This is a targeted re-review. An earlier version had these blocking findings:
%s
Check whether each of them is still present. Report an issue only if it still exists or if you find a NEW blocking problem (P0/P1) introduced by this version. Do not report style or non-blocking issues in a targeted review. If everything is clean, reply {"findings":[]}.`, prior.String())
	}
	content := artifact.Result
	if len(content) > 6<<10 {
		content = content[:6<<10] + "\n... (truncated to fit the prompt limit)"
	}
	return fmt.Sprintf(`You are a strict reviewer. You see exactly one artifact and nothing else.

Task objective: %s
Expected artifact file: %s
Files changed by this run: %s
Dependency files already accepted. They are injected into the workspace, so they may exist or appear
in the diff; they are inputs and never an extra file: %s

Content of that file (raw, between markers):
<<<CONTENT
%s
CONTENT>>>

Review scope: %s

The artifact file itself is the expected output: creating or changing that one file is never a side effect. A side effect means any OTHER file or change.
A previously reported extra file is resolved when it no longer appears in "Files this run changed".

Rules:
- P0: security issue, data loss, or destructive behavior.
- P1: wrong result, contract violation, extra files or side effects not asked for.
- P2: maintainability, naming, missing explanation.
- P3: nit.
Judge only the artifact content and the task objective. Do not use tools. Do not modify files.

Reply with JSON only, no prose and no code fences:
{"findings":[{"severity":"P1","code":"short_slug","message":"..."}]}
If there is nothing to report, reply {"findings":[]}.`, task.Objective, task.ArtifactPath, strings.Join(artifact.Files, ", "), strings.Join(req.Deps, ", "), content, scope)
}

type wireFinding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

func parseFindings(text string) ([]domain.Finding, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object in reviewer reply")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text[start:end+1]), &payload); err != nil {
		return nil, fmt.Errorf("parse reviewer JSON: %w", err)
	}
	raw, ok := payload["findings"]
	if !ok {
		return nil, fmt.Errorf("reviewer JSON has no findings field")
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, fmt.Errorf("reviewer findings is null")
	}
	var list []wireFinding
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("reviewer findings must be an array: %w", err)
	}
	out := make([]domain.Finding, 0, len(list))
	for _, f := range list {
		sev := domain.Severity(strings.ToUpper(strings.TrimSpace(f.Severity)))
		switch sev {
		case domain.SeverityP0, domain.SeverityP1, domain.SeverityP2, domain.SeverityP3:
		default:
			sev = domain.SeverityP1 // unknown severity must not pass silently
		}
		code := strings.TrimSpace(f.Code)
		if code == "" {
			code = "unspecified"
		}
		out = append(out, domain.Finding{Severity: sev, Code: code, Message: strings.TrimSpace(f.Message)})
	}
	return out, nil
}
