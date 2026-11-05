// Package exec defines the only seam between the Kernel and anything that runs
// work. The Kernel never spawns processes itself.
package exec

import (
	"context"
	"fmt"
	"sync"

	"projectos/kernel/internal/domain"
)

type StartRequest struct {
	TaskID       domain.TaskID
	Attempt      int
	Prompt       string
	Workdir      string
	ArtifactPath string
	// Key, when set, replaces the attempt number in the backend's idempotency
	// key. Reviewers use it so each reviewed artifact state gets its own session.
	Key string
	// ApprovalMode is passed through to the backend when set. An empty value
	// keeps the backend default; reviewers use "default" so tool use needs a
	// human approval instead of auto-approval.
	ApprovalMode string
	// SessionID resumes a provider conversation on a retry. Empty means a new
	// session; reviewers and planners always start fresh.
	SessionID string
}

type StartResult struct {
	ExternalID string
}

type State struct {
	Status   domain.RunStatus
	Message  string
	Evidence string
}

// ReviewRequest tells a reviewer what to look at and how wide its scope is.
type ReviewRequest struct {
	Task     domain.Task
	Artifact Artifact
	Kind     domain.ReviewKind
	Codes    []string         // non-empty only for targeted re-reviews
	Prior    []domain.Finding // blocking findings of the previous full review
	RunID    string           // unique per run; used for session idempotency
	Deps     []string         // dependency artifact paths already accepted
}

type Artifact struct {
	Result  string
	Version string
	// Files are the workspace paths touched by this run, so a reviewer can see
	// side effects the artifact content alone would hide.
	Files []string
	// PromptChars/OutputChars are kept for older backends and for the UI's
	// character counts. Tokens are authoritative when the backend reports them;
	// UsageEstimated says whether they came from a provider or a heuristic.
	PromptChars    int
	OutputChars    int
	PromptTokens   int
	OutputTokens   int
	UsageEstimated bool
	SessionID      string
}

type Backend interface {
	Name() string
	Start(ctx context.Context, req StartRequest) (StartResult, error)
	Observe(ctx context.Context, externalID string) (State, error)
	Cancel(ctx context.Context, externalID string) error
	Collect(ctx context.Context, externalID, artifactPath string) (Artifact, error)
}

// FakeBackend completes each run after a fixed number of observations. It only
// proves Kernel control flow; it says nothing about real agent execution.
type FakeBackend struct {
	Ticks  int
	Output map[domain.TaskID]string
	Fail   map[domain.TaskID]bool
	// Files, when set for a task, becomes the workspace file list Collect
	// reports. Tests use it to imitate a worktree that contains injected
	// dependency files.
	Files map[domain.TaskID][]string
	// Starts records every StartRequest so tests can inspect the prompt a retry
	// actually received.
	Starts []StartRequest

	mu       sync.Mutex
	pending  map[string]int
	active   map[string]bool
	results  map[string]string
	versions map[string]string
	files    map[string][]string
	failed   map[string]bool
	calls    int
	peak     int
}

func NewFake(ticks int) *FakeBackend {
	return &FakeBackend{Ticks: ticks, Output: map[domain.TaskID]string{}, Fail: map[domain.TaskID]bool{},
		pending: map[string]int{}, active: map[string]bool{}, results: map[string]string{},
		versions: map[string]string{}, files: map[string][]string{}, failed: map[string]bool{}}
}

func (f *FakeBackend) Name() string { return "fake" }

func (f *FakeBackend) Start(_ context.Context, req StartRequest) (StartResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Starts = append(f.Starts, req)
	id := fmt.Sprintf("fake-%s-%d", req.TaskID, req.Attempt)
	f.pending[id] = f.Ticks
	f.results[id] = f.Output[req.TaskID]
	f.versions[id] = "fake:" + id
	if files, ok := f.Files[req.TaskID]; ok {
		f.files[id] = files
	}
	f.failed[id] = f.Fail[req.TaskID]
	f.active[id] = true
	if len(f.active) > f.peak {
		f.peak = len(f.active)
	}
	f.calls++
	return StartResult{ExternalID: id}, nil
}

func (f *FakeBackend) Observe(_ context.Context, externalID string) (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	left, ok := f.pending[externalID]
	if !ok {
		return State{}, fmt.Errorf("unknown run %s", externalID)
	}
	left--
	f.pending[externalID] = left
	if left > 0 {
		return State{Status: domain.RunRunning}, nil
	}
	delete(f.active, externalID)
	status := domain.RunSucceeded
	if f.failed[externalID] {
		status = domain.RunFailed
	}
	return State{Status: status, Evidence: "fake"}, nil
}

func (f *FakeBackend) Cancel(_ context.Context, externalID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pending, externalID)
	return nil
}

func (f *FakeBackend) Collect(_ context.Context, externalID, _ string) (Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	files := f.files[externalID]
	if len(files) == 0 {
		files = []string{"fake/" + externalID}
	}
	return Artifact{
		Result:         f.results[externalID],
		Version:        f.versions[externalID],
		Files:          files,
		PromptChars:    120,
		OutputChars:    80,
		PromptTokens:   30,
		OutputTokens:   20,
		UsageEstimated: true,
	}, nil
}

// PeakConcurrent reports the highest number of simultaneously active runs.
func (f *FakeBackend) PeakConcurrent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peak
}
