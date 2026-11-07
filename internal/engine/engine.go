// Package engine owns scheduling decisions, verification and the bounded
// failure policy. It is the only component allowed to move a Task to accepted.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/store"
	"projectos/kernel/internal/verify"
)

type ValidatorFunc func(result string, deps map[domain.TaskID]string) error

type Engine struct {
	store          *store.Store
	stateDir       string
	sessionResume  bool
	backend        exec.Backend
	maxWorkers     int
	validators     map[string]ValidatorFunc
	acceptance     Reviewer
	objectiveJudge ObjectiveJudge
	budget         Budget
	reviewer       Reviewer
	runTimeout     time.Duration
	observeErr     map[string]int
	quarantine     string
}

const quarantineKey = "quarantine"

// Budget bounds how much real work one Objective may trigger.
type Budget struct {
	MaxRuns    int       // 0 = unlimited
	MaxReviews int       // 0 = unlimited
	MaxTokens  int       // estimated tokens (chars/4); 0 = unlimited
	Deadline   time.Time // zero = no deadline
}

// ReviewOutcome carries findings plus the reviewer's own usage, so the token
// budget cannot be dodged by spending tokens on reviews.
type ReviewOutcome struct {
	Findings       []domain.Finding
	PromptChars    int
	OutputChars    int
	PromptTokens   int
	OutputTokens   int
	UsageEstimated bool
}

// Reviewer produces findings for one artifact version. It never changes Task
// status; the Engine decides what a finding means.
type Reviewer interface {
	Review(ctx context.Context, req exec.ReviewRequest) (ReviewOutcome, error)
}

// ObjectiveReviewRequest is the goal-level question. Per-task acceptance asks
// "did this artifact fulfil its task"; this asks "does the delivered set fulfil
// the original objective". It is deliberately separate from the task reviewer.
type ObjectiveReviewRequest struct {
	ObjectiveID     string
	Goal            string
	ExpectedOutcome string
	Acceptance      []string
	TaskResults     map[string]string // artifact path -> accepted content
	TaskSummary     []string
}

// ObjectiveJudge is optional. When configured, an objective is never completed
// merely because every task passed; the goal-level judgment must also pass.
type ObjectiveJudge interface {
	JudgeObjective(ctx context.Context, req ObjectiveReviewRequest) (ReviewOutcome, error)
}

// observeErrorBudget is how many consecutive observe failures are tolerated
// before a run is treated as unknown. A single network blip must not kill a task.
const observeErrorBudget = 3

func New(st *store.Store, backend exec.Backend, maxWorkers int, validators map[string]ValidatorFunc) *Engine {
	if maxWorkers <= 0 {
		maxWorkers = 1
	}
	return &Engine{store: st, backend: backend, maxWorkers: maxWorkers, validators: validators,
		observeErr: map[string]int{}, sessionResume: true}
}

// WithStateDir lets quarantine survive a database write failure via a file.
// WithSessionResume makes retries continue the provider session. It is on by
// default; a reviewer still gets a fresh session, and a caller can opt out when
// the backend cannot safely resume across work directories.
func (e *Engine) WithSessionResume(on bool) *Engine {
	e.sessionResume = on
	return e
}

func (e *Engine) WithStateDir(dir string) *Engine {
	e.stateDir = dir
	return e
}

func (e *Engine) WithBudget(b Budget) *Engine {
	// Negative limits are meaningless; treat them as "unlimited" instead of
	// letting a typo silently disable the guard.
	if b.MaxRuns < 0 {
		b.MaxRuns = 0
	}
	if b.MaxReviews < 0 {
		b.MaxReviews = 0
	}
	if b.MaxTokens < 0 {
		b.MaxTokens = 0
	}
	e.budget = b
	return e
}

// WithRunTimeout bounds how long a single run may stay in flight. A run that
// exceeds it is cancelled and blocked, never retried silently.
func (e *Engine) WithRunTimeout(d time.Duration) *Engine {
	if d < 0 {
		d = 0
	}
	e.runTimeout = d
	return e
}

// WithAcceptance adds a second judgment: the reviewer asks "is this defective?",
// acceptance asks "did this actually accomplish the task?".
func (e *Engine) WithAcceptance(r Reviewer) *Engine {
	e.acceptance = r
	return e
}

func (e *Engine) WithObjectiveJudge(j ObjectiveJudge) *Engine {
	e.objectiveJudge = j
	return e
}

func (e *Engine) WithReviewer(r Reviewer) *Engine {
	e.reviewer = r
	return e
}

// SubmitReview records a review through the bounded-review policy.
func (e *Engine) SubmitReview(rev domain.Review) error {
	return e.store.AddReview(rev)
}

func (e *Engine) Add(t domain.Task) error {
	return e.store.CreateTask(t)
}

type StepResult struct {
	Started  []domain.TaskID
	Accepted []domain.TaskID
	Rejected []domain.TaskID
	Blocked  []domain.TaskID
}

// Step performs exactly one scheduling round: settle finished runs, verify them,
// then start as many ready tasks as the worker budget allows.
func (e *Engine) Step(ctx context.Context) (StepResult, error) {
	var out StepResult
	if e.store == nil || e.backend == nil {
		return out, fmt.Errorf("engine is not wired: store=%v backend=%v", e.store != nil, e.backend != nil)
	}

	active, err := e.store.ActiveRuns()
	if err != nil {
		return out, err
	}
	for _, run := range active {
		if run.ExternalID == "" {
			continue
		}
		task, ok, err := e.store.Get(run.TaskID)
		if err != nil {
			return out, err
		}
		if !ok {
			return out, fmt.Errorf("run %s references unknown task %s", run.ID, run.TaskID)
		}
		if e.runTimeout > 0 && !run.StartedAt.IsZero() && time.Since(run.StartedAt) > e.runTimeout {
			note := "run timeout"
			if cerr := e.cancelWithRetry(ctx, run.ExternalID); cerr != nil {
				note = "run timeout; cancel failed: " + cerr.Error()
				e.record(run.TaskID, "cancel_failed", cerr.Error())
				// The worker may still be running and spending. Stop scheduling
				// anything new until a human looks at it.
				e.setQuarantine("quarantined: cancel failed for " + string(run.TaskID))
			}
			out.Blocked = e.appendBlocked(out.Blocked, run.TaskID, "run timeout")
			if _, err := e.settle(run, task, exec.Artifact{}, domain.RunUnknown, false, false, note); err != nil {
				return out, err
			}
			e.record(run.TaskID, "run_timeout", e.runTimeout.String())
			continue
		}

		state, err := e.backend.Observe(ctx, run.ExternalID)
		if err != nil {
			e.observeErr[run.ID]++
			if e.observeErr[run.ID] < observeErrorBudget {
				e.record(run.TaskID, "observe_retry", err.Error())
				continue
			}
			delete(e.observeErr, run.ID)
			out.Rejected = append(out.Rejected, run.TaskID)
			out.Blocked = e.appendBlocked(out.Blocked, run.TaskID, "observe error")
			if _, serr := e.settle(run, task, exec.Artifact{}, domain.RunUnknown, false, false, "observe error"); serr != nil {
				return out, serr
			}
			e.record(run.TaskID, "run_unknown", err.Error())
			continue
		}
		delete(e.observeErr, run.ID)
		if state.Status == domain.RunRunning {
			continue
		}

		artifact, cerr := e.backend.Collect(ctx, run.ExternalID, task.ArtifactPath)
		if cerr != nil {
			out.Rejected = append(out.Rejected, run.TaskID)
			out.Blocked = e.appendBlocked(out.Blocked, run.TaskID, "collect error")
			// Keep whatever usage the backend could still report, so a failed read
			// does not silently erase tokens already spent.
			if _, serr := e.settle(run, task, artifact, domain.RunUnknown, false, false, "collect: "+cerr.Error()); serr != nil {
				return out, serr
			}
			e.record(run.TaskID, "collect_failed", cerr.Error())
			if artifact.PromptChars+artifact.OutputChars+artifact.PromptTokens+artifact.OutputTokens == 0 {
				// Spend for this run is unknown; stop the objective rather than
				// letting the remaining tasks run on an under-counted budget.
				blocked, berr := e.store.BlockPending("budget: usage unknown after collect failure")
				if berr != nil {
					return out, berr
				}
				for _, id := range blocked {
					e.record(id, "blocked", "usage unknown")
					out.Blocked = append(out.Blocked, id)
				}
				return out, nil
			}
			continue
		}

		switch state.Status {
		case domain.RunSucceeded:
			if err := e.verify(run.TaskID, artifact); err != nil {
				next, serr := e.settle(run, task, artifact, domain.RunSucceeded, false, true, "verification: "+err.Error())
				if serr != nil {
					return out, serr
				}
				out.Rejected = append(out.Rejected, run.TaskID)
				if next == domain.TaskBlocked {
					out.Blocked = append(out.Blocked, run.TaskID)
				}
				e.record(run.TaskID, "rejected", err.Error())
				continue
			}
			next, blocking, err := e.review(ctx, run, task, artifact)
			if err != nil {
				return out, err
			}
			if blocking {
				out.Rejected = append(out.Rejected, run.TaskID)
				if next == domain.TaskBlocked {
					out.Blocked = append(out.Blocked, run.TaskID)
				}
				continue
			}
			if _, serr := e.settle(run, task, artifact, domain.RunSucceeded, true, false, ""); serr != nil {
				return out, serr
			}
			out.Accepted = append(out.Accepted, run.TaskID)
			e.record(run.TaskID, "accepted", artifact.Result)
		case domain.RunFailed:
			next, serr := e.settle(run, task, artifact, domain.RunFailed, false, true, "worker failed")
			if serr != nil {
				return out, serr
			}
			out.Rejected = append(out.Rejected, run.TaskID)
			if next == domain.TaskBlocked {
				out.Blocked = append(out.Blocked, run.TaskID)
			}
		default: // cancelled / unknown: never auto-retry an unresolved outcome
			out.Rejected = append(out.Rejected, run.TaskID)
			out.Blocked = e.appendBlocked(out.Blocked, run.TaskID, string(state.Status))
			if _, serr := e.settle(run, task, artifact, state.Status, false, false, string(state.Status)+": "+state.Message); serr != nil {
				return out, serr
			}
			e.record(run.TaskID, "run_"+string(state.Status), state.Message)
		}
	}

	if reason, err := e.budgetStop(); err != nil {
		return out, err
	} else if reason != "" {
		blocked, err := e.store.BlockPending(reason)
		if err != nil {
			return out, err
		}
		for _, id := range blocked {
			e.record(id, "blocked", reason)
			out.Blocked = append(out.Blocked, id)
		}
		return out, nil
	}

	active, err = e.store.ActiveRuns()
	if err != nil {
		return out, err
	}
	free := e.maxWorkers - len(active)
	if free <= 0 {
		return out, nil
	}
	ready, err := e.store.Ready()
	if err != nil {
		return out, err
	}
	for i := 0; i < len(ready) && i < free; i++ {
		if reason, err := e.budgetStop(); err != nil {
			return out, err
		} else if reason != "" {
			break
		}
		task := ready[i]
		runID := fmt.Sprintf("%s-run-%d-%d", task.ID, task.Attempts+1, time.Now().UnixNano())
		run, ok, err := e.store.Claim(task.ID, runID, e.backend.Name())
		if err != nil {
			return out, err
		}
		if !ok {
			continue // another controller claimed it first
		}
		prompt, perr := e.expandPrompt(task)
		if perr != nil {
			_, _ = e.settle(run, task, exec.Artifact{}, domain.RunFailed, false, false, "prompt: "+perr.Error())
			out.Rejected = append(out.Rejected, task.ID)
			out.Blocked = e.appendBlocked(out.Blocked, task.ID, "prompt")
			continue
		}
		session := ""
		if e.sessionResume && task.Attempts > 0 {
			session, _ = e.store.LastSession(task.ID, e.backend.Name())
		}
		started, serr := e.backend.Start(ctx, exec.StartRequest{
			TaskID: task.ID, Attempt: run.Attempt, Prompt: prompt, ArtifactPath: task.ArtifactPath,
			SessionID: session,
		})
		if serr != nil {
			// A failed Start may still have created a session; never retry blindly.
			_, _ = e.settle(run, task, exec.Artifact{}, domain.RunFailed, false, false, "start: "+serr.Error())
			out.Rejected = append(out.Rejected, task.ID)
			out.Blocked = e.appendBlocked(out.Blocked, task.ID, "start")
			e.record(task.ID, "start_failed", serr.Error())
			continue
		}
		if err := e.store.SetExternal(run.ID, started.ExternalID); err != nil {
			_ = e.backend.Cancel(ctx, started.ExternalID)
			_, _ = e.settle(run, task, exec.Artifact{}, domain.RunFailed, false, false, "record external id: "+err.Error())
			out.Rejected = append(out.Rejected, task.ID)
			out.Blocked = e.appendBlocked(out.Blocked, task.ID, "external id")
			continue
		}
		e.record(task.ID, "started", run.ID)
		out.Started = append(out.Started, task.ID)
	}

	blocked, err := e.store.BlockDependents("dependency blocked")
	if err != nil {
		return out, err
	}
	for _, id := range blocked {
		e.record(id, "blocked", "dependency blocked")
		out.Blocked = append(out.Blocked, id)
	}
	return out, nil
}

// review runs the reviewer (if any) and reports whether acceptance is blocked.
func (e *Engine) review(ctx context.Context, run domain.Run, task domain.Task, artifact exec.Artifact) (domain.TaskStatus, bool, error) {
	if e.reviewer == nil && e.acceptance == nil {
		return "", false, nil
	}
	reviewVersion := reviewFingerprint(artifact)

	// Dependency files are injected into the worker's tree, so they appear in the
	// diff. Both gates must know they are inputs, not outputs.
	ancestors, err := e.ancestors(task)
	if err != nil {
		return "", false, err
	}
	var depPaths []string
	depSet := map[string]bool{}
	for _, dep := range ancestors {
		depTask, ok, err := e.store.Get(dep)
		if err != nil {
			return "", false, err
		}
		if !ok || depTask.ArtifactPath == "" {
			continue
		}
		depPaths = append(depPaths, depTask.ArtifactPath)
		depSet[filepath.Clean(depTask.ArtifactPath)] = true
	}
	if len(depSet) > 0 && len(artifact.Files) > 0 {
		kept := make([]string, 0, len(artifact.Files))
		for _, file := range artifact.Files {
			if !depSet[filepath.Clean(file)] {
				kept = append(kept, file)
			}
		}
		artifact.Files = kept
	}

	if e.reviewer != nil {
		used, err := e.store.CountReviews()
		if err != nil {
			return "", false, err
		}
		if e.budget.MaxReviews > 0 && used >= e.budget.MaxReviews {
			next, serr := e.settle(run, task, artifact, domain.RunSucceeded, false, false, "budget: review limit reached")
			if serr != nil {
				return "", false, serr
			}
			e.record(run.TaskID, "review_budget", "review limit reached")
			return next, true, nil
		}

		kind := domain.ReviewFull
		codes, err := e.store.LastBlockingCodes(run.TaskID)
		if err != nil {
			return "", false, err
		}
		var prior []domain.Finding
		if run.Attempt > 1 && len(codes) > 0 {
			kind = domain.ReviewTargeted
			prior, err = e.store.LastBlockingFindings(run.TaskID)
			if err != nil {
				return "", false, err
			}
		}
		outcome, rerr := e.reviewer.Review(ctx, exec.ReviewRequest{
			Task: task, Artifact: artifact, Kind: kind, Codes: codes, Prior: prior, RunID: run.ID, Deps: depPaths,
		})
		findings := outcome.Findings
		if rerr != nil {
			findings = []domain.Finding{{Severity: domain.SeverityP1, Code: "reviewer_error", Message: rerr.Error()}}
			if outcome.PromptChars+outcome.OutputChars+outcome.PromptTokens+outcome.OutputTokens == 0 {
				// The session may have spent tokens we cannot see; stop the objective
				// instead of letting other tasks run on a bad estimate.
				next, serr := e.settle(run, task, artifact, domain.RunSucceeded, false, false, "review failed with unknown usage")
				if serr != nil {
					return "", false, serr
				}
				if err := e.stopUnknownUsage("budget: usage unknown after reviewer failure"); err != nil {
					return "", false, err
				}
				return next, true, nil
			}
		}
		findings = normalizeFindings(findings)
		if kind == domain.ReviewTargeted {
			findings = blockingOnly(findings)
		}
		if err := e.store.AddReview(domain.Review{
			TaskID:          run.TaskID,
			ArtifactVersion: reviewVersion,
			Kind:            kind,
			PlanVersion:     task.PlanVersion,
			Findings:        findings,
			PromptChars:     outcome.PromptChars,
			OutputChars:     outcome.OutputChars,
			PromptTokens:    outcome.PromptTokens,
			OutputTokens:    outcome.OutputTokens,
			UsageEstimated:  outcome.UsageEstimated,
		}); err != nil {
			next, serr := e.settle(run, task, artifact, domain.RunSucceeded, false, false, "review not recorded: "+err.Error())
			if serr != nil {
				return "", false, serr
			}
			e.record(run.TaskID, "review_unrecorded", err.Error())
			if uerr := e.stopUnknownUsage("budget: usage unknown after unrecorded review"); uerr != nil {
				return "", false, uerr
			}
			return next, true, nil
		}
		blocking, err := e.store.HasBlockingReview(run.TaskID, reviewVersion)
		if err != nil {
			return "", false, err
		}
		if blocking {
			next, serr := e.settle(run, task, artifact, domain.RunSucceeded, false, true, "review: blocking finding (P0/P1)")
			if serr != nil {
				return "", false, serr
			}
			e.record(run.TaskID, "review_blocked", "P0/P1 on "+reviewVersion)
			return next, true, nil
		}
	}
	return e.judge(ctx, run, task, artifact, depPaths)
}

// judge runs the acceptance agent on the same artifact version. It is the last
// gate before acceptance, and it fails closed.
func (e *Engine) judge(ctx context.Context, run domain.Run, task domain.Task, artifact exec.Artifact, deps []string) (domain.TaskStatus, bool, error) {
	if e.acceptance == nil {
		return "", false, nil
	}
	version := reviewFingerprint(artifact)

	used, err := e.store.CountReviews()
	if err != nil {
		return "", false, err
	}
	if e.budget.MaxReviews > 0 && used >= e.budget.MaxReviews {
		next, serr := e.settle(run, task, artifact, domain.RunSucceeded, false, false, "budget: review limit reached")
		if serr != nil {
			return "", false, serr
		}
		e.record(run.TaskID, "review_budget", "review limit reached")
		return next, true, nil
	}

	outcome, jerr := e.acceptance.Review(ctx, exec.ReviewRequest{
		Task: task, Artifact: artifact, Kind: domain.ReviewAcceptance, RunID: run.ID, Deps: deps,
	})
	findings := normalizeFindings(outcome.Findings)
	if jerr != nil {
		findings = []domain.Finding{{Severity: domain.SeverityP1, Code: "acceptance_error", Message: jerr.Error()}}
		if outcome.PromptChars+outcome.OutputChars+outcome.PromptTokens+outcome.OutputTokens == 0 {
			next, serr := e.settle(run, task, artifact, domain.RunSucceeded, false, false, "acceptance failed with unknown usage")
			if serr != nil {
				return "", false, serr
			}
			if uerr := e.stopUnknownUsage("budget: usage unknown after acceptance failure"); uerr != nil {
				return "", false, uerr
			}
			return next, true, nil
		}
	}
	if err := e.store.AddReview(domain.Review{
		TaskID:          run.TaskID,
		ArtifactVersion: version,
		Kind:            domain.ReviewAcceptance,
		PlanVersion:     task.PlanVersion,
		Findings:        findings,
		PromptChars:     outcome.PromptChars,
		OutputChars:     outcome.OutputChars,
		PromptTokens:    outcome.PromptTokens,
		OutputTokens:    outcome.OutputTokens,
		UsageEstimated:  outcome.UsageEstimated,
	}); err != nil {
		// This artifact version was already judged. Reuse the acceptance verdict
		// itself: a later clean targeted review must not erase an earlier block.
		if errors.Is(err, store.ErrReviewLimit) {
			blocking, found, berr := e.store.AcceptanceVerdict(run.TaskID, version)
			if berr != nil {
				return "", false, berr
			}
			if !found || blocking {
				next, serr := e.settle(run, task, artifact, domain.RunSucceeded, false, true, "acceptance: task not fulfilled")
				if serr != nil {
					return "", false, serr
				}
				e.record(run.TaskID, "acceptance_blocked", "on "+version)
				return next, true, nil
			}
			return "", false, nil
		}
		next, serr := e.settle(run, task, artifact, domain.RunSucceeded, false, false, "acceptance not recorded: "+err.Error())
		if serr != nil {
			return "", false, serr
		}
		return next, true, nil
	}

	blocking, found, err := e.store.AcceptanceVerdict(run.TaskID, version)
	if err != nil {
		return "", false, err
	}
	if !found || !blocking {
		return "", false, nil
	}
	next, serr := e.settle(run, task, artifact, domain.RunSucceeded, false, true, "acceptance: task not fulfilled")
	if serr != nil {
		return "", false, serr
	}
	e.record(run.TaskID, "acceptance_blocked", "on "+version)
	return next, true, nil
}

// Abort stops every in-flight run and closes the books. Cancellation failures
// are recorded, never hidden: a worker may still be running and burning budget.
func (e *Engine) Abort(ctx context.Context, reason string) (int, error) {
	if e.store == nil || e.backend == nil {
		return 0, fmt.Errorf("engine is not wired")
	}
	abortCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	active, err := e.store.ActiveRuns()
	if err != nil {
		return 0, err
	}
	details := map[domain.TaskID]string{}
	finished := map[domain.TaskID]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, run := range active {
		if run.ExternalID == "" {
			continue
		}
		run := run
		wg.Add(1)
		go func() {
			defer wg.Done()
			cerr := e.cancelWithRetry(abortCtx, run.ExternalID)
			mu.Lock()
			defer mu.Unlock()
			finished[run.TaskID] = true
			if cerr != nil {
				details[run.TaskID] = "cancel failed: " + cerr.Error()
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-abortCtx.Done():
		mu.Lock()
		for _, run := range active {
			if !finished[run.TaskID] {
				details[run.TaskID] = "cancel did not finish before the abort deadline"
			}
		}
		mu.Unlock()
	}
	// Copy under the lock: a late Cancel may still write to details after this
	// point, and passing the live map to the store would race with it.
	mu.Lock()
	snapshot := make(map[domain.TaskID]string, len(details))
	for id, detail := range details {
		snapshot[id] = detail
	}
	mu.Unlock()
	for id, detail := range snapshot {
		e.record(id, "cancel_failed", detail)
	}
	if len(snapshot) > 0 {
		e.setQuarantine("quarantined: abort could not confirm every worker stopped")
	}
	return e.store.AbandonRunning(reason, snapshot)
}

func (e *Engine) cancelWithRetry(ctx context.Context, externalID string) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = e.backend.Cancel(callCtx, externalID)
		cancel()
		if err == nil {
			return nil
		}
		if attempt == 0 {
			select {
			case <-ctx.Done():
				return err
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
	return err
}

// stopUnknownUsage blocks every pending task when the amount already spent is
// unknown, so a budget cannot be silently exceeded.
func (e *Engine) stopUnknownUsage(note string) error {
	blocked, err := e.store.BlockPending(note)
	if err != nil {
		return err
	}
	for _, id := range blocked {
		e.record(id, "blocked", note)
	}
	return nil
}

func (e *Engine) settle(run domain.Run, task domain.Task, artifact exec.Artifact,
	runStatus domain.RunStatus, accepted, allowRetry bool, note string) (domain.TaskStatus, error) {
	next, err := e.store.SettleRun(store.Settlement{
		RunID:          run.ID,
		RunStatus:      runStatus,
		Result:         artifact.Result,
		Note:           note,
		Accepted:       accepted,
		AllowRetry:     allowRetry,
		PromptChars:    artifact.PromptChars,
		OutputChars:    artifact.OutputChars,
		PromptTokens:   artifact.PromptTokens,
		OutputTokens:   artifact.OutputTokens,
		UsageEstimated: artifact.UsageEstimated,
		SessionID:      artifact.SessionID,
		Evidence: domain.Evidence{
			ArtifactPath: task.ArtifactPath,
			// Identify the artifact state, not AO's compare base: two attempts with
			// identical a.txt but different side effects must not look identical.
			ArtifactVersion: reviewFingerprint(artifact),
		},
	})
	if errors.Is(err, store.ErrStaleRun) {
		return "", nil // a late result: the task already moved on
	}
	return next, err
}

func (e *Engine) appendBlocked(list []domain.TaskID, id domain.TaskID, reason string) []domain.TaskID {
	e.record(id, "blocked", reason)
	return append(list, id)
}

func blockingOnly(findings []domain.Finding) []domain.Finding {
	out := make([]domain.Finding, 0, len(findings))
	for _, f := range findings {
		if f.Severity == domain.SeverityP0 || f.Severity == domain.SeverityP1 {
			out = append(out, f)
		}
	}
	return out
}

// retryFindings returns only a blocking review of the exact artifact state that
// just failed. An older review of a previous version must not hide a newer
// verification error from the retry prompt.
func (e *Engine) retryFindings(task domain.Task) ([]domain.Finding, error) {
	evidence, err := e.store.EvidenceFor(task.ID)
	if err != nil {
		return nil, err
	}
	version := ""
	for _, ev := range evidence {
		version = ev.ArtifactVersion
	}
	if version == "" {
		return e.store.LastReviewFindings(task.ID)
	}
	reviews, err := e.store.ReviewsFor(task.ID)
	if err != nil {
		return nil, err
	}
	for i := len(reviews) - 1; i >= 0; i-- {
		if reviews[i].ArtifactVersion == version && reviews[i].Blocking() {
			return reviews[i].Findings, nil
		}
	}
	return nil, nil
}

// reviewFingerprint binds a review to the exact artifact state a reviewer saw.
func reviewFingerprint(artifact exec.Artifact) string {
	files := append([]string(nil), artifact.Files...)
	sort.Strings(files)
	sum := sha256.Sum256([]byte(artifact.Result + "\x00" + strings.Join(files, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// quarantineReason reads the persisted flag so a restart cannot silently resume
// scheduling while an external worker may still be running.
func (e *Engine) quarantineReason() (string, error) {
	if e.quarantine != "" {
		return e.quarantine, nil
	}
	reason, err := e.store.GetMeta(quarantineKey)
	if err != nil {
		return "", err
	}
	e.quarantine = reason
	return reason, nil
}

func (e *Engine) setQuarantine(reason string) {
	e.quarantine = reason
	for attempt := 0; attempt < 3; attempt++ {
		if err := e.store.SetMeta(quarantineKey, reason); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The database write failed; a file in the state dir keeps the decision
	// across restarts, which is the whole point of quarantine.
	if e.stateDir != "" {
		if err := os.WriteFile(e.quarantineFile(), []byte(reason), 0o644); err == nil {
			e.record("", "quarantine_persisted_to_file", "database write failed")
			return
		}
	}
	e.record("", "quarantine_persist_failed", "could not persist quarantine")
}

func (e *Engine) quarantineFile() string { return filepath.Join(e.stateDir, "QUARANTINE") }

// RestoreQuarantine loads a file-based quarantine left by an earlier process.
func (e *Engine) RestoreQuarantine() string {
	if e.stateDir == "" {
		return ""
	}
	raw, err := os.ReadFile(e.quarantineFile())
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		return ""
	}
	reason := strings.TrimSpace(string(raw))
	e.setQuarantine(reason)
	return reason
}

func (e *Engine) ClearQuarantine() error {
	// Persist first: clearing memory before the durable flag would let the next
	// restart resume scheduling while the old flag still says quarantined.
	if err := e.store.SetMeta(quarantineKey, ""); err != nil {
		return err
	}
	if e.stateDir != "" {
		if err := os.Remove(e.quarantineFile()); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	e.quarantine = ""
	return nil
}

func (e *Engine) budgetStop() (string, error) {
	reason, err := e.quarantineReason()
	if err != nil {
		return "", err
	}
	if reason != "" {
		return reason, nil
	}
	if !e.budget.Deadline.IsZero() && time.Now().After(e.budget.Deadline) {
		return "budget: deadline exceeded", nil
	}
	if e.budget.MaxRuns > 0 {
		n, err := e.store.RunCount()
		if err != nil {
			return "", fmt.Errorf("budget check: %w", err)
		}
		if n >= e.budget.MaxRuns {
			return "budget: run limit reached", nil
		}
	}
	if e.budget.MaxTokens > 0 {
		promptTokens, outputTokens, _, err := e.store.UsageTokens()
		if err != nil {
			return "", fmt.Errorf("usage check: %w", err)
		}
		if promptTokens+outputTokens >= e.budget.MaxTokens {
			return "budget: token limit reached", nil
		}
	}
	return "", nil
}

// expandPrompt substitutes ${TASK_ID} and ${ATTEMPT}, and carries the previous
// rejection reason into a retry so the worker can actually fix it.
func (e *Engine) expandPrompt(task domain.Task) (string, error) {
	prompt := strings.ReplaceAll(task.Prompt, "${ATTEMPT}", fmt.Sprintf("%d", task.Attempts+1))
	if task.Attempts > 0 {
		prompt += "\nThis is a retry in a new working directory. Work from the current directory and ignore file paths from earlier attempts."
	}
	for _, dep := range task.Deps {
		value, err := e.store.AcceptedResult(dep)
		if err != nil {
			return "", fmt.Errorf("dependency %s has no accepted artifact", dep)
		}
		prompt = strings.ReplaceAll(prompt, "${"+string(dep)+"}", strings.TrimSpace(value))
	}
	if task.Attempts > 0 {
		// A retry that only hears "a blocking finding happened" has to guess. Give
		// it the reviewer's actual words, or it burns another round rediscovering
		// the same defect.
		findings, ferr := e.retryFindings(task)
		if ferr != nil {
			return "", ferr
		}
		if block := feedbackBlock(findings); block != "" {
			prompt += block
		} else if strings.TrimSpace(task.LastError) != "" {
			prompt += "\n\nYour previous attempt was rejected: " + task.LastError +
				"\nFix exactly that problem and do not repeat it."
		}
	}
	// Give the worker the dependency artifacts it must build against: its own
	// worktree starts from the base branch and does not contain them.
	budget := 6 << 10 // AO rejects prompts over 16 KiB including its system prompt
	ancestors, err := e.ancestors(task)
	if err != nil {
		return "", err
	}
	for _, dep := range ancestors {
		value, err := e.store.AcceptedResult(dep)
		if err != nil {
			return "", fmt.Errorf("dependency %s has no accepted artifact", dep)
		}
		depTask, ok, err := e.store.Get(dep)
		if err != nil {
			return "", err
		}
		if !ok || depTask.ArtifactPath == "" {
			continue
		}
		content := value
		if len(content) > 4<<10 {
			content = content[:4<<10] + "\n... (truncated)"
		}
		if budget-len(content) < 0 {
			break
		}
		budget -= len(content)
		prompt += "\n\nDependency file " + depTask.ArtifactPath + " is already accepted; do not recreate or modify it.\n\n<<<FILE\n" + content + "\nFILE>>>"
	}
	return prompt, nil
}

func (e *Engine) verify(id domain.TaskID, artifact exec.Artifact) error {
	task, ok, err := e.store.Get(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("unknown task %s", id)
	}
	deps := map[domain.TaskID]string{}
	for _, d := range task.Deps {
		value, err := e.store.AcceptedResult(d)
		if err != nil {
			return fmt.Errorf("dependency %s has no accepted result", d)
		}
		deps[d] = value
	}
	if check, ok := e.validators[task.Validator]; ok {
		if err := check(artifact.Result, deps); err != nil {
			return err
		}
	} else if len(task.Commands) == 0 {
		return fmt.Errorf("no validator %q", task.Validator)
	}
	return e.runCommands(task, artifact, deps)
}

// normalizeFindings treats an unrecognised severity as blocking. A reviewer that
// invents "P99" must not be able to wave a defect through.
func normalizeFindings(in []domain.Finding) []domain.Finding {
	if len(in) == 0 {
		return in
	}
	out := make([]domain.Finding, 0, len(in))
	for _, f := range in {
		switch f.Severity {
		case domain.SeverityP0, domain.SeverityP1, domain.SeverityP2, domain.SeverityP3:
		default:
			f.Severity = domain.SeverityP1
			if f.Code == "" {
				f.Code = "unknown_severity"
			}
		}
		out = append(out, f)
	}
	return out
}

// feedbackBlock renders review findings for the next attempt. Blocking findings
// are instructions; P2/P3 are suggestions that are cheap to fix now.
func feedbackBlock(findings []domain.Finding) string {
	const perFinding = 400
	var blocking, minor []string
	for _, f := range findings {
		text := strings.TrimSpace(f.Message)
		if len(text) > perFinding {
			text = text[:perFinding] + "…"
		}
		line := "[" + string(f.Severity) + "] " + f.Code + ": " + text
		if f.Severity == domain.SeverityP0 || f.Severity == domain.SeverityP1 {
			blocking = append(blocking, line)
		} else {
			minor = append(minor, line)
		}
	}
	var b strings.Builder
	if len(blocking) > 0 {
		b.WriteString("\n\nThe reviewer rejected your previous attempt. Fix exactly these findings:\n")
		for _, line := range blocking {
			b.WriteString("- " + line + "\n")
		}
		b.WriteString("The same output will be rejected again.")
	}
	if len(minor) > 0 {
		b.WriteString("\n\nNon-blocking review notes (fix them if cheap):\n")
		for _, line := range minor {
			b.WriteString("- " + line + "\n")
		}
	}
	return b.String()
}

// ancestors returns every transitive dependency, so a task two hops away still
// receives the files it needs.
func (e *Engine) ancestors(task domain.Task) ([]domain.TaskID, error) {
	seen := map[domain.TaskID]bool{}
	queue := append([]domain.TaskID(nil), task.Deps...)
	var out []domain.TaskID
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		dep, ok, err := e.store.Get(id)
		if err != nil {
			return nil, err
		}
		if ok {
			queue = append(queue, dep.Deps...)
		}
	}
	return out, nil
}

// runCommands materializes the artifact plus its dependencies in a throw-away
// directory and runs allow-listed commands there. A test that cannot even start
// must fail the task.
func (e *Engine) runCommands(task domain.Task, artifact exec.Artifact, deps map[domain.TaskID]string) error {
	if len(task.Commands) == 0 {
		return nil
	}
	sandbox, err := os.MkdirTemp("", "kernel-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(sandbox)
	write := func(rel, content string) error {
		clean := filepath.Clean(rel)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("refusing to write outside sandbox: %q", rel)
		}
		target := filepath.Join(sandbox, clean)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, []byte(content), 0o644)
	}
	inputs := map[domain.TaskID]string{}
	for id, content := range deps {
		inputs[id] = content
	}
	ancestors, err := e.ancestors(task)
	if err != nil {
		return err
	}
	for _, id := range ancestors {
		if _, ok := inputs[id]; ok {
			continue
		}
		content, err := e.store.AcceptedResult(id)
		if err != nil {
			continue
		}
		inputs[id] = content
	}
	total := len(artifact.Result)
	for _, content := range inputs {
		total += len(content)
	}
	if total > 1<<20 {
		return fmt.Errorf("verification inputs exceed 1MB")
	}
	if err := write(task.ArtifactPath, artifact.Result); err != nil {
		return err
	}
	for depID, content := range inputs {
		depTask, ok, err := e.store.Get(depID)
		if err != nil {
			return err
		}
		if !ok || depTask.ArtifactPath == "" {
			continue
		}
		if err := write(depTask.ArtifactPath, content); err != nil {
			return err
		}
	}
	for _, spec := range task.Commands {
		if err := verify.RunCommand(sandbox, spec); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) record(id domain.TaskID, kind, detail string) {
	_ = e.store.AppendEvent(id, kind, detail)
}

// Run ticks until every task is terminal or the budget is exhausted.
func (e *Engine) Run(ctx context.Context, maxSteps int) (int, error) {
	for i := 0; i < maxSteps; i++ {
		if _, err := e.Step(ctx); err != nil {
			return i, err
		}
		tasks, err := e.store.List()
		if err != nil {
			return i, err
		}
		done := true
		for _, t := range tasks {
			switch t.Status {
			case domain.TaskAccepted, domain.TaskBlocked, domain.TaskSuperseded:
			default:
				done = false
			}
		}
		if done {
			return i + 1, nil
		}
	}
	return maxSteps, fmt.Errorf("engine did not converge within %d steps", maxSteps)
}
