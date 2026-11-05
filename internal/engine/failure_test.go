package engine_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/engine"
	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/exec/ao"
	"projectos/kernel/internal/store"
)

type failingBackend struct{}

func (failingBackend) Name() string { return "failing" }
func (failingBackend) Start(context.Context, exec.StartRequest) (exec.StartResult, error) {
	return exec.StartResult{}, errors.New("daemon unreachable")
}
func (failingBackend) Observe(context.Context, string) (exec.State, error) {
	return exec.State{}, errors.New("daemon unreachable")
}
func (failingBackend) Cancel(context.Context, string) error { return nil }
func (failingBackend) Collect(context.Context, string, string) (exec.Artifact, error) {
	return exec.Artifact{}, errors.New("daemon unreachable")
}

func TestStartFailureBlocksTask(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, failingBackend{}, 2, validators())
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 10); err != nil {
		t.Fatalf("run: %v", err)
	}
	task, _, _ := st.Get("A")
	if task.Status != domain.TaskBlocked {
		t.Fatalf("a backend that cannot start must block the task, got %s", task.Status)
	}
}

func TestRunTimeoutBlocksHangingRun(t *testing.T) {
	fake := exec.NewFake(1000) // never finishes within the test window
	fake.Output["A"] = "A-OK"
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, fake, 1, validators()).WithRunTimeout(20 * time.Millisecond)
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatalf("timeout step: %v", err)
	}
	task, _, _ := st.Get("A")
	if task.Status != domain.TaskBlocked {
		t.Fatalf("a timed-out run must block the task, got %s", task.Status)
	}
	if task.LastError != "run timeout" {
		t.Fatalf("last error = %q", task.LastError)
	}
}

type flakyBackend struct {
	fake     *exec.FakeBackend
	failures int
	calls    int
}

func (f *flakyBackend) Name() string { return "flaky" }
func (f *flakyBackend) Start(ctx context.Context, req exec.StartRequest) (exec.StartResult, error) {
	return f.fake.Start(ctx, req)
}
func (f *flakyBackend) Observe(ctx context.Context, id string) (exec.State, error) {
	f.calls++
	if f.calls <= f.failures {
		return exec.State{}, errors.New("temporary network error")
	}
	return f.fake.Observe(ctx, id)
}
func (f *flakyBackend) Cancel(ctx context.Context, id string) error { return f.fake.Cancel(ctx, id) }
func (f *flakyBackend) Collect(ctx context.Context, id, p string) (exec.Artifact, error) {
	return f.fake.Collect(ctx, id, p)
}

func TestTransientObserveErrorsAreTolerated(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"] = "A-OK"
	backend := &flakyBackend{fake: fake, failures: 2} // below the budget of 3
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, backend, 1, validators())
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 20); err != nil {
		t.Fatalf("run: %v", err)
	}
	task, _, _ := st.Get("A")
	if task.Status != domain.TaskAccepted {
		t.Fatalf("two blips must not kill the task, got %s (%s)", task.Status, task.LastError)
	}
}

func TestPersistentObserveErrorsBlock(t *testing.T) {
	fake := exec.NewFake(1)
	backend := &flakyBackend{fake: fake, failures: 99}
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, backend, 1, validators())
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 20); err != nil {
		t.Fatalf("run: %v", err)
	}
	task, _, _ := st.Get("A")
	if task.Status != domain.TaskBlocked || task.LastError != "observe error" {
		t.Fatalf("persistent errors must block: %s (%s)", task.Status, task.LastError)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetMeta("objective_hash", "abc"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMeta("objective_hash", "def"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetMeta("objective_hash")
	if err != nil || got != "def" {
		t.Fatalf("meta = %q, %v", got, err)
	}
	missing, err := st.GetMeta("nope")
	if err != nil || missing != "" {
		t.Fatalf("missing meta = %q, %v", missing, err)
	}
}

type cancelFailBackend struct {
	fake *exec.FakeBackend
}

func (c *cancelFailBackend) Name() string { return "cancel-fail" }
func (c *cancelFailBackend) Start(ctx context.Context, r exec.StartRequest) (exec.StartResult, error) {
	return c.fake.Start(ctx, r)
}
func (c *cancelFailBackend) Observe(ctx context.Context, id string) (exec.State, error) {
	return exec.State{Status: domain.RunRunning}, nil
}
func (c *cancelFailBackend) Cancel(context.Context, string) error {
	return errors.New("cancel rejected")
}
func (c *cancelFailBackend) Collect(context.Context, string, string) (exec.Artifact, error) {
	return exec.Artifact{}, nil
}

func TestCancelFailureIsRecorded(t *testing.T) {
	fake := exec.NewFake(1)
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, &cancelFailBackend{fake: fake}, 1, validators()).WithRunTimeout(10 * time.Millisecond)
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	task, _, _ := st.Get("A")
	if task.Status != domain.TaskBlocked {
		t.Fatalf("timeout must block, got %s", task.Status)
	}
	if !strings.Contains(task.LastError, "cancel failed") {
		t.Fatalf("cancel failure must be recorded, got %q", task.LastError)
	}
}

func TestAbortClosesInFlightRuns(t *testing.T) {
	fake := exec.NewFake(1000)
	fake.Output["A"] = "A-OK"
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, fake, 1, validators())
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	n, err := e.Abort(context.Background(), "objective timeout")
	if err != nil || n != 1 {
		t.Fatalf("abort = %d, %v", n, err)
	}
	task, _, _ := st.Get("A")
	if task.Status != domain.TaskBlocked {
		t.Fatalf("aborted task must be blocked, got %s", task.Status)
	}
	runs, _ := st.ActiveRuns()
	if len(runs) != 0 {
		t.Fatalf("no run should remain active, got %d", len(runs))
	}
}

type unknownBackend struct{ fake *exec.FakeBackend }

func (u *unknownBackend) Name() string { return "unknown" }
func (u *unknownBackend) Start(ctx context.Context, r exec.StartRequest) (exec.StartResult, error) {
	return u.fake.Start(ctx, r)
}
func (u *unknownBackend) Observe(context.Context, string) (exec.State, error) {
	return exec.State{Status: domain.RunUnknown}, nil
}
func (u *unknownBackend) Cancel(context.Context, string) error { return nil }
func (u *unknownBackend) Collect(context.Context, string, string) (exec.Artifact, error) {
	return exec.Artifact{}, nil
}

func TestUnknownRunIsNotRetried(t *testing.T) {
	fake := exec.NewFake(1)
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, &unknownBackend{fake: fake}, 1, validators())
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A", MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 10); err != nil {
		t.Fatalf("run: %v", err)
	}
	task, _, _ := st.Get("A")
	if task.Status != domain.TaskBlocked {
		t.Fatalf("unknown outcome must block, got %s", task.Status)
	}
	if task.Attempts != 1 {
		t.Fatalf("unknown outcome must not be retried, attempts=%d", task.Attempts)
	}
}

func TestTokenBudgetBlocksRemainingTasks(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"], fake.Output["B"] = "A-OK", "B-OK"
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// Each fake run reports 200 chars = 50 estimated tokens, so a 50-token budget
	// allows exactly one run; the check can only react after usage is recorded.
	e := engine.New(st, fake, 1, validators()).WithBudget(engine.Budget{MaxTokens: 50})
	for _, id := range []domain.TaskID{"A", "B"} {
		if err := e.Add(domain.Task{ID: id, Prompt: "p", Validator: "exact:" + string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Run(context.Background(), 20); err != nil {
		t.Fatalf("run: %v", err)
	}
	runs, _ := st.RunCount()
	if runs != 1 {
		t.Fatalf("token budget should allow one run, got %d", runs)
	}
	b, _, _ := st.Get("B")
	if b.Status != domain.TaskBlocked {
		t.Fatalf("B should be blocked by the token budget, got %s", b.Status)
	}
}

func TestCancelFailureStopsFurtherScheduling(t *testing.T) {
	fake := exec.NewFake(1)
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, &cancelFailBackend{fake: fake}, 1, validators()).WithRunTimeout(10 * time.Millisecond)
	for _, id := range []domain.TaskID{"A", "B"} {
		if err := e.Add(domain.Task{ID: id, Prompt: "p", Validator: "exact:" + string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _, _ := st.Get("B")
	if b.Attempts != 0 {
		t.Fatalf("a failed cancel must stop new work, B attempts=%d", b.Attempts)
	}
	if b.Status != domain.TaskBlocked {
		t.Fatalf("B should be blocked by quarantine, got %s", b.Status)
	}
}

func TestAbortRecordsCancelFailureOnTask(t *testing.T) {
	fake := exec.NewFake(1000)
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, &cancelFailBackend{fake: fake}, 1, validators())
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Abort(context.Background(), "objective timeout"); err != nil {
		t.Fatal(err)
	}
	task, _, _ := st.Get("A")
	if !strings.Contains(task.LastError, "cancel failed") {
		t.Fatalf("abort must keep the cancel error visible, got %q", task.LastError)
	}
}

type partialUsageBackend struct {
	fake *exec.FakeBackend
}

func (p *partialUsageBackend) Name() string { return "partial" }
func (p *partialUsageBackend) Start(ctx context.Context, r exec.StartRequest) (exec.StartResult, error) {
	return p.fake.Start(ctx, r)
}
func (p *partialUsageBackend) Observe(context.Context, string) (exec.State, error) {
	return exec.State{Status: domain.RunSucceeded}, nil
}
func (p *partialUsageBackend) Cancel(context.Context, string) error { return nil }
func (p *partialUsageBackend) Collect(context.Context, string, string) (exec.Artifact, error) {
	return exec.Artifact{PromptChars: 400, OutputChars: 200}, errors.New("file read failed")
}

func TestCollectFailureKeepsKnownUsage(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, &partialUsageBackend{fake: exec.NewFake(1)}, 1, validators())
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 10); err != nil {
		t.Fatalf("run: %v", err)
	}
	prompt, output, err := st.UsageChars()
	if err != nil {
		t.Fatal(err)
	}
	if prompt != 400 || output != 200 {
		t.Fatalf("usage from a failed collect must be kept, got %d/%d", prompt, output)
	}
}

func TestQuarantineSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "k.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fake := exec.NewFake(1)
	e := engine.New(st, &cancelFailBackend{fake: fake}, 1, validators()).WithRunTimeout(10 * time.Millisecond)
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Restart: quarantine must still block new work.
	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if err := st2.CreateTask(domain.Task{ID: "B", Prompt: "p", Validator: "exact:B"}); err != nil {
		t.Fatal(err)
	}
	e2 := engine.New(st2, &cancelFailBackend{fake: exec.NewFake(1)}, 1, validators())
	if _, err := e2.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _, _ := st2.Get("B")
	if b.Attempts != 0 || b.Status != domain.TaskBlocked {
		t.Fatalf("persisted quarantine must block new work, got %s attempts=%d", b.Status, b.Attempts)
	}
}

type unknownUsageBackend struct{}

func (unknownUsageBackend) Name() string { return "unknown-usage" }
func (unknownUsageBackend) Start(context.Context, exec.StartRequest) (exec.StartResult, error) {
	return exec.StartResult{ExternalID: "x"}, nil
}
func (unknownUsageBackend) Observe(context.Context, string) (exec.State, error) {
	return exec.State{Status: domain.RunSucceeded}, nil
}
func (unknownUsageBackend) Cancel(context.Context, string) error { return nil }
func (unknownUsageBackend) Collect(context.Context, string, string) (exec.Artifact, error) {
	return exec.Artifact{}, errors.New("artifact unreadable")
}

func TestUnknownUsageStopsRemainingTasks(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, unknownUsageBackend{}, 1, validators())
	for _, id := range []domain.TaskID{"A", "B"} {
		if err := e.Add(domain.Task{ID: id, Prompt: "p", Validator: "exact:" + string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Run(context.Background(), 10); err != nil {
		t.Fatalf("run: %v", err)
	}
	b, _, _ := st.Get("B")
	if b.Attempts != 0 {
		t.Fatalf("unknown usage must stop new runs, B attempts=%d", b.Attempts)
	}
	if b.Status != domain.TaskBlocked {
		t.Fatalf("B should be blocked, got %s", b.Status)
	}
}

type slowCancelBackend struct{}

func (slowCancelBackend) Name() string { return "slow-cancel" }
func (slowCancelBackend) Start(context.Context, exec.StartRequest) (exec.StartResult, error) {
	return exec.StartResult{ExternalID: "x"}, nil
}
func (slowCancelBackend) Observe(context.Context, string) (exec.State, error) {
	return exec.State{Status: domain.RunRunning}, nil
}
func (slowCancelBackend) Cancel(context.Context, string) error {
	time.Sleep(600 * time.Millisecond) // ignores context on purpose
	return errors.New("still running")
}
func (slowCancelBackend) Collect(context.Context, string, string) (exec.Artifact, error) {
	return exec.Artifact{}, nil
}

func TestAbortRespectsDeadline(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, slowCancelBackend{}, 1, validators())
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := e.Abort(ctx, "objective timeout"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("abort exceeded its deadline: %s", elapsed)
	}
	task, _, _ := st.Get("A")
	if task.Status != domain.TaskBlocked || !strings.Contains(task.LastError, "cancel") {
		t.Fatalf("abort must block with a cancel note, got %s / %q", task.Status, task.LastError)
	}
	// Let the late Cancel goroutine finish so -race can see any shared-map write.
	time.Sleep(800 * time.Millisecond)
	if q, _ := st.GetMeta("quarantine"); q == "" {
		t.Fatal("a failed cancel during abort must persist quarantine")
	}
}

func setStartedAt(t *testing.T, path, runID, when string) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`UPDATE runs SET started_at=? WHERE id=?`, when, runID); err != nil {
		t.Fatal(err)
	}
}

func TestClockSkewDoesNotExpireFutureRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fake := exec.NewFake(1)
	fake.Output["A"] = "A-OK"
	e := engine.New(st, fake, 1, validators()).WithRunTimeout(10 * time.Millisecond)
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	runs, _ := st.ActiveRuns()
	if len(runs) != 1 {
		t.Fatalf("expected one active run, got %d", len(runs))
	}
	setStartedAt(t, path, runs[0].ID, "2999-01-01T00:00:00Z")
	time.Sleep(20 * time.Millisecond) // wall time passes, but the run looks far in the future
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	task, _, _ := st.Get("A")
	if task.Status != domain.TaskAccepted {
		t.Fatalf("a future timestamp must not be treated as expired, got %s (%s)", task.Status, task.LastError)
	}
}

func TestClockSkewExpiresOldRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fake := exec.NewFake(1000)
	e := engine.New(st, fake, 1, validators()).WithRunTimeout(10 * time.Millisecond)
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	runs, _ := st.ActiveRuns()
	setStartedAt(t, path, runs[0].ID, "2000-01-01T00:00:00Z")
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	task, _, _ := st.Get("A")
	if task.Status != domain.TaskBlocked {
		t.Fatalf("an old timestamp must expire the run, got %s", task.Status)
	}
}

// A daemon that dies after the session was created must not be retried blindly:
// the engine tolerates a few observe failures, then blocks the task.
func TestDaemonDiesMidRun(t *testing.T) {
	var killed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions" {
			_, _ = w.Write([]byte(`{"session":{"id":"s1"}}`))
			return
		}
		killed.Store(true)
		panic(http.ErrAbortHandler) // connection drops: daemon is gone
	}))
	defer srv.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	backend := ao.New(ao.Config{BaseURL: srv.URL, ProjectID: "repo", Model: "fake/model", StartTimeout: 2 * time.Second})
	e := engine.New(st, backend, 1, validators())
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := e.Step(context.Background()); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if !killed.Load() {
		t.Fatal("observe was never attempted")
	}
	task, _, _ := st.Get("A")
	if task.Status != domain.TaskBlocked {
		t.Fatalf("a dead daemon must block the task, got %s", task.Status)
	}
}
