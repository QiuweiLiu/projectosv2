package ao

import (
	"context"
	"strings"
	"testing"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/exec"
)

// flakyBackend fails the first session with a provider error and succeeds on
// the next one. It proves the retry loop, not a real provider.
type flakyBackend struct {
	calls  int
	prompt []string
	keys   []string
}

func (f *flakyBackend) Name() string { return "flaky" }

func (f *flakyBackend) Start(_ context.Context, req exec.StartRequest) (exec.StartResult, error) {
	f.calls++
	f.prompt = append(f.prompt, req.Prompt)
	f.keys = append(f.keys, req.Key)
	return exec.StartResult{ExternalID: "run-" + string(rune('0'+f.calls))}, nil
}

func (f *flakyBackend) Observe(context.Context, string) (exec.State, error) {
	if f.calls == 1 {
		return exec.State{Status: domain.RunFailed, Evidence: "HTTP 429: rate limited"}, nil
	}
	return exec.State{Status: domain.RunSucceeded}, nil
}

func (f *flakyBackend) Cancel(context.Context, string) error { return nil }

func (f *flakyBackend) Collect(_ context.Context, externalID, _ string) (exec.Artifact, error) {
	if externalID == "run-1" {
		return exec.Artifact{PromptTokens: 7, OutputTokens: 3}, nil
	}
	return exec.Artifact{Result: `{"findings":[]}`, PromptTokens: 11, OutputTokens: 5}, nil
}

func TestRunSessionRetriesProviderRateLimit(t *testing.T) {
	defer func(old time.Duration) { retryBackoff = old }(retryBackoff)
	retryBackoff = time.Millisecond
	backend := &flakyBackend{}
	req := exec.ReviewRequest{
		Task:     domain.Task{ID: "T1", Objective: "g", ArtifactPath: "a.txt"},
		Artifact: exec.Artifact{Result: "42", Files: []string{"a.txt"}},
		Kind:     domain.ReviewFull,
		RunID:    "run",
	}
	out, err := runSession(context.Background(), backend, req, "reviewer", "prompt", 2*time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("retry should recover: %v", err)
	}
	if len(out.Findings) != 0 {
		t.Fatalf("findings = %+v", out.Findings)
	}
	if backend.calls != 2 {
		t.Fatalf("calls = %d", backend.calls)
	}
	if len(backend.keys) != 2 || backend.keys[0] == backend.keys[1] {
		t.Fatalf("retry reused the session key: %v", backend.keys)
	}
	if out.PromptTokens != 18 || out.OutputTokens != 8 {
		t.Fatalf("usage not accumulated: %+v", out)
	}
}

func TestRunSessionDoesNotRetryPermanentFailure(t *testing.T) {
	defer func(old time.Duration) { retryBackoff = old }(retryBackoff)
	retryBackoff = time.Millisecond
	backend := &permanentBackend{}
	req := exec.ReviewRequest{
		Task:     domain.Task{ID: "T1", Objective: "g", ArtifactPath: "a.txt"},
		Artifact: exec.Artifact{Result: "42", Files: []string{"a.txt"}},
		Kind:     domain.ReviewFull,
		RunID:    "run",
	}
	out, err := runSession(context.Background(), backend, req, "reviewer", "prompt", 2*time.Second, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("permanent error must surface: %v", err)
	}
	if len(out.Findings) != 1 || out.Findings[0].Severity != domain.SeverityP1 {
		t.Fatalf("must fail closed: %+v", out.Findings)
	}
	if backend.calls != 1 {
		t.Fatalf("permanent failures must not be retried: %d calls", backend.calls)
	}
}

type permanentBackend struct{ calls int }

func (p *permanentBackend) Name() string { return "permanent" }
func (p *permanentBackend) Start(context.Context, exec.StartRequest) (exec.StartResult, error) {
	p.calls++
	return exec.StartResult{ExternalID: "run-1"}, nil
}
func (p *permanentBackend) Observe(context.Context, string) (exec.State, error) {
	return exec.State{Status: domain.RunFailed, Evidence: "permission denied"}, nil
}
func (p *permanentBackend) Cancel(context.Context, string) error { return nil }
func (p *permanentBackend) Collect(context.Context, string, string) (exec.Artifact, error) {
	return exec.Artifact{PromptTokens: 4, OutputTokens: 2}, nil
}
