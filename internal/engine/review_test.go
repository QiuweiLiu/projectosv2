package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/engine"
	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/store"
)

type scriptedReviewer struct {
	responses [][]domain.Finding
	requests  []exec.ReviewRequest
}

func (s *scriptedReviewer) Review(_ context.Context, req exec.ReviewRequest) (engine.ReviewOutcome, error) {
	s.requests = append(s.requests, req)
	outcome := engine.ReviewOutcome{PromptChars: 100, OutputChars: 50}
	if len(s.requests) <= len(s.responses) {
		outcome.Findings = s.responses[len(s.requests)-1]
	}
	return outcome, nil
}

func TestBlockingReviewStopsAcceptanceAndDependent(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"], fake.Output["B"], fake.Output["C"] = "A-OK", "B-OK", "A-OK+B-OK"
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, fake, 2, validators()).WithReviewer(&scriptedReviewer{
		responses: [][]domain.Finding{{{Severity: domain.SeverityP1, Code: "leak", Message: "resource leak"}}},
	})
	diamond(t, e, 1)
	if _, err := e.Run(context.Background(), 50); err != nil {
		t.Fatalf("run: %v", err)
	}
	a, _, _ := st.Get("A")
	c, _, _ := st.Get("C")
	if a.Status != domain.TaskBlocked {
		t.Fatalf("A must be blocked by a P1 finding, got %s", a.Status)
	}
	if c.Status != domain.TaskBlocked {
		t.Fatalf("C must not run, got %s", c.Status)
	}
	reviews, _ := st.ReviewsFor("A")
	if len(reviews) != 1 || !reviews[0].Blocking() {
		t.Fatalf("expected one blocking review, got %#v", reviews)
	}
}

func TestNonBlockingReviewStillAccepts(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"], fake.Output["B"], fake.Output["C"] = "A-OK", "B-OK", "A-OK+B-OK"
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, fake, 2, validators()).WithReviewer(&scriptedReviewer{
		responses: [][]domain.Finding{{{Severity: domain.SeverityP2, Code: "naming"}}},
	})
	diamond(t, e, 1)
	if _, err := e.Run(context.Background(), 50); err != nil {
		t.Fatalf("run: %v", err)
	}
	a, _, _ := st.Get("A")
	if a.Status != domain.TaskAccepted {
		t.Fatalf("P2 must not block acceptance, got %s", a.Status)
	}
}

func TestRetryUsesTargetedReviewWithPriorCodes(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"] = "A-OK"
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reviewer := &scriptedReviewer{responses: [][]domain.Finding{
		{{Severity: domain.SeverityP1, Code: "bug"}},
		{},
	}}
	e := engine.New(st, fake, 1, validators()).WithReviewer(reviewer)
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A", MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 30); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(reviewer.requests) != 2 {
		t.Fatalf("expected 2 reviews, got %d", len(reviewer.requests))
	}
	first, second := reviewer.requests[0], reviewer.requests[1]
	if first.Kind != domain.ReviewFull || len(first.Codes) != 0 {
		t.Fatalf("first review = %#v", first)
	}
	if second.Kind != domain.ReviewTargeted || len(second.Codes) != 1 || second.Codes[0] != "bug" {
		t.Fatalf("second review = %#v", second)
	}
	task, _, _ := st.Get("A")
	if task.Status != domain.TaskAccepted {
		t.Fatalf("task should be accepted after a clean targeted review, got %s", task.Status)
	}
}

func TestReviewBudgetBlocksFurtherReviews(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"], fake.Output["B"] = "A-OK", "B-OK"
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reviewer := &scriptedReviewer{}
	e := engine.New(st, fake, 2, validators()).WithReviewer(reviewer).WithBudget(engine.Budget{MaxReviews: 1})
	for _, id := range []domain.TaskID{"A", "B"} {
		if err := e.Add(domain.Task{ID: id, Prompt: "p", Validator: "exact:" + string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Run(context.Background(), 30); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(reviewer.requests) != 1 {
		t.Fatalf("review budget 1 should allow exactly one review, got %d", len(reviewer.requests))
	}
	blocked := 0
	for _, id := range []domain.TaskID{"A", "B"} {
		task, _, _ := st.Get(id)
		if task.Status == domain.TaskBlocked {
			blocked++
		}
	}
	if blocked != 1 {
		t.Fatalf("expected one task blocked by the review budget, got %d", blocked)
	}
}

func TestTargetedReviewCanIntroduceNewBlockingFinding(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"] = "A-OK"
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reviewer := &scriptedReviewer{responses: [][]domain.Finding{
		{{Severity: domain.SeverityP1, Code: "old_problem"}},
		{{Severity: domain.SeverityP1, Code: "brand_new"}},
	}}
	e := engine.New(st, fake, 1, validators()).WithReviewer(reviewer)
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A", MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 30); err != nil {
		t.Fatalf("run: %v", err)
	}
	task, _, _ := st.Get("A")
	if task.Status != domain.TaskBlocked {
		t.Fatalf("a new P1 in a targeted review must block, got %s", task.Status)
	}
	if len(reviewer.requests) != 2 || reviewer.requests[1].Kind != domain.ReviewTargeted {
		t.Fatalf("second review should be targeted: %#v", reviewer.requests)
	}
}

func TestReviewUsageCountsTowardBudget(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"], fake.Output["B"], fake.Output["C"] = "A-OK", "B-OK", "A-OK+B-OK"
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, fake, 2, validators()).WithReviewer(&scriptedReviewer{})
	diamond(t, e, 1)
	if _, err := e.Run(context.Background(), 50); err != nil {
		t.Fatalf("run: %v", err)
	}
	prompt, output, err := st.UsageChars()
	if err != nil {
		t.Fatal(err)
	}
	// three reviews x 150 chars plus three fake runs x 200 chars
	if prompt < 450 || output < 150 {
		t.Fatalf("review usage missing from totals: prompt=%d output=%d", prompt, output)
	}
}

type errorReviewer struct{}

func (errorReviewer) Review(context.Context, exec.ReviewRequest) (engine.ReviewOutcome, error) {
	return engine.ReviewOutcome{PromptChars: 120, OutputChars: 30}, errors.New("reviewer crashed")
}

type zeroUsageErrorReviewer struct{}

func (zeroUsageErrorReviewer) Review(context.Context, exec.ReviewRequest) (engine.ReviewOutcome, error) {
	return engine.ReviewOutcome{}, errors.New("reviewer timed out before reporting usage")
}

func TestReviewerFailureWithUnknownUsageStopsScheduling(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"], fake.Output["B"] = "A-OK", "B-OK"
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, fake, 1, validators()).WithReviewer(zeroUsageErrorReviewer{})
	for _, id := range []domain.TaskID{"A", "B"} {
		if err := e.Add(domain.Task{ID: id, Prompt: "p", Validator: "exact:" + string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Run(context.Background(), 10); err != nil {
		t.Fatalf("run: %v", err)
	}
	b, _, _ := st.Get("B")
	if b.Attempts != 0 || b.Status != domain.TaskBlocked {
		t.Fatalf("unknown usage must stop scheduling, B=%s attempts=%d", b.Status, b.Attempts)
	}
}

func TestReviewerFailureStillCountsUsage(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"] = "A-OK"
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, fake, 1, validators()).WithReviewer(errorReviewer{})
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "exact:A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 10); err != nil {
		t.Fatalf("run: %v", err)
	}
	task, _, _ := st.Get("A")
	if task.Status != domain.TaskBlocked {
		t.Fatalf("reviewer failure must block, got %s", task.Status)
	}
	prompt, output, err := st.UsageChars()
	if err != nil {
		t.Fatal(err)
	}
	if prompt < 120 || output < 30 {
		t.Fatalf("reviewer usage lost on failure: %d/%d", prompt, output)
	}
}

// A task two hops from the seed still receives that seed's file, and its
// worktree therefore contains it. The reviewer must know it is an input, not an
// unrequested side effect.
func TestReviewerSeesTransitiveDependencyFiles(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"], fake.Output["B"], fake.Output["C"] = "seed", "mid", "done"
	fake.Files = map[domain.TaskID][]string{"C": {"seed.txt", "mid.txt", "out.md"}}

	reviewer := &scriptedReviewer{}
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	accept := func(string, map[domain.TaskID]string) error { return nil }
	e := engine.New(st, fake, 1, map[string]engine.ValidatorFunc{"ok": accept}).WithReviewer(reviewer)

	for _, task := range []domain.Task{
		{ID: "A", Prompt: "seed", Validator: "ok", ArtifactPath: "seed.txt"},
		{ID: "B", Prompt: "mid", Validator: "ok", ArtifactPath: "mid.txt", Deps: []domain.TaskID{"A"}},
		{ID: "C", Prompt: "out", Validator: "ok", ArtifactPath: "out.md", Deps: []domain.TaskID{"B"}},
	} {
		if err := e.Add(task); err != nil {
			t.Fatalf("add %s: %v", task.ID, err)
		}
	}
	if _, err := e.Run(context.Background(), 40); err != nil {
		t.Fatalf("run: %v", err)
	}

	var got *exec.ReviewRequest
	for i := range reviewer.requests {
		if reviewer.requests[i].Task.ID == "C" {
			got = &reviewer.requests[i]
		}
	}
	if got == nil {
		var seen []string
		for _, r := range reviewer.requests {
			seen = append(seen, string(r.Task.ID)+"/"+string(r.Task.Status))
		}
		t.Fatalf("C was never reviewed; requests=%v", seen)
	}
	deps := strings.Join(got.Deps, ",")
	if !strings.Contains(deps, "seed.txt") || !strings.Contains(deps, "mid.txt") {
		t.Fatalf("review deps = %v, want the transitive closure", got.Deps)
	}
	if len(got.Artifact.Files) != 1 || got.Artifact.Files[0] != "out.md" {
		t.Fatalf("review files = %v, want only the task's own artifact", got.Artifact.Files)
	}
}

// A blocked attempt must retry with the reviewer's actual findings. "A blocking
// finding happened" is not something a worker can act on.
func TestRetryPromptCarriesReviewerFindings(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"] = "x"
	reviewer := &scriptedReviewer{responses: [][]domain.Finding{
		{{Severity: domain.SeverityP1, Code: "csv_columns_ignored", Message: "parse the amount column explicitly"}},
		nil,
	}}

	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	accept := func(string, map[domain.TaskID]string) error { return nil }
	e := engine.New(st, fake, 1, map[string]engine.ValidatorFunc{"ok": accept}).WithReviewer(reviewer)
	if err := e.Add(domain.Task{ID: "A", Prompt: "write it", Validator: "ok", ArtifactPath: "a.txt", MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 40); err != nil {
		t.Fatalf("run: %v", err)
	}

	var retry string
	for _, start := range fake.Starts {
		if start.TaskID == "A" && start.Attempt > 1 {
			retry = start.Prompt
		}
	}
	if retry == "" {
		t.Fatal("no retry was started")
	}
	for _, want := range []string{"csv_columns_ignored", "parse the amount column explicitly", "Fix exactly these findings"} {
		if !strings.Contains(retry, want) {
			t.Fatalf("retry prompt is missing %q:\n%s", want, retry)
		}
	}
}

// A reviewer that invents a severity must not pass the gate.
func TestUnknownSeverityIsBlocking(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"] = "x"
	reviewer := &scriptedReviewer{responses: [][]domain.Finding{
		{{Severity: domain.Severity("P99"), Code: "made_up", Message: "looks fine probably"}},
	}}
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	accept := func(string, map[domain.TaskID]string) error { return nil }
	e := engine.New(st, fake, 1, map[string]engine.ValidatorFunc{"ok": accept}).WithReviewer(reviewer)
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "ok", ArtifactPath: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 20); err != nil {
		t.Fatalf("run: %v", err)
	}
	task, _, _ := st.Get("A")
	if task.Status == domain.TaskAccepted {
		t.Fatal("a finding with an unknown severity was accepted")
	}
}

// The acceptance judge asks a different question from the reviewer. A clean
// review plus a failed acceptance must still block the task.
func TestAcceptanceFailureBlocksEvenWhenReviewPasses(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"] = "x"
	reviewer := &scriptedReviewer{responses: [][]domain.Finding{nil}}
	judge := &scriptedReviewer{responses: [][]domain.Finding{
		{{Severity: domain.SeverityP1, Code: "missing_requirement", Message: "the task asked for three rows"}},
	}}

	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	accept := func(string, map[domain.TaskID]string) error { return nil }
	e := engine.New(st, fake, 1, map[string]engine.ValidatorFunc{"ok": accept}).
		WithReviewer(reviewer).WithAcceptance(judge)
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "ok", ArtifactPath: "a.txt", MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 20); err != nil {
		t.Fatalf("run: %v", err)
	}
	task, _, _ := st.Get("A")
	if task.Status == domain.TaskAccepted {
		t.Fatal("a failed acceptance was accepted")
	}
	if !strings.Contains(task.LastError, "acceptance") {
		t.Fatalf("last error should name acceptance, got %q", task.LastError)
	}
}

// Acceptance must run on its own: disabling the reviewer must not disable the
// completion judge.
func TestAcceptanceRunsWithoutReviewer(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"] = "x"
	judge := &scriptedReviewer{responses: [][]domain.Finding{
		{{Severity: domain.SeverityP1, Code: "missing_rows", Message: "only one row"}},
	}}
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	accept := func(string, map[domain.TaskID]string) error { return nil }
	e := engine.New(st, fake, 1, map[string]engine.ValidatorFunc{"ok": accept}).WithAcceptance(judge)
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "ok", ArtifactPath: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 20); err != nil {
		t.Fatalf("run: %v", err)
	}
	task, _, _ := st.Get("A")
	if task.Status == domain.TaskAccepted {
		t.Fatal("acceptance rejection was skipped because the reviewer was off")
	}
}

// The same artifact version judged twice must keep the earlier block. A record
// conflict is not a pass.
func TestRepeatedAcceptanceOnSameArtifactStaysBlocked(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"] = "same"
	judge := &scriptedReviewer{responses: [][]domain.Finding{
		{{Severity: domain.SeverityP1, Code: "missing_rows", Message: "only one row"}},
		{{Severity: domain.SeverityP1, Code: "missing_rows", Message: "only one row"}},
	}}
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	accept := func(string, map[domain.TaskID]string) error { return nil }
	e := engine.New(st, fake, 1, map[string]engine.ValidatorFunc{"ok": accept}).WithAcceptance(judge)
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "ok", ArtifactPath: "a.txt", MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 40); err != nil {
		t.Fatalf("run: %v", err)
	}
	task, _, _ := st.Get("A")
	if task.Status == domain.TaskAccepted {
		t.Fatal("identical artifact became accepted after a second rejection")
	}
}

// Review budget covers every judgment session, acceptance included.
func TestAcceptanceRespectsReviewBudget(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"] = "x"
	reviewer := &scriptedReviewer{responses: [][]domain.Finding{nil}}
	judge := &scriptedReviewer{responses: [][]domain.Finding{nil}}
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	accept := func(string, map[domain.TaskID]string) error { return nil }
	e := engine.New(st, fake, 1, map[string]engine.ValidatorFunc{"ok": accept}).
		WithReviewer(reviewer).WithAcceptance(judge).
		WithBudget(engine.Budget{MaxReviews: 1})
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "ok", ArtifactPath: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 20); err != nil {
		t.Fatalf("run: %v", err)
	}
	task, _, _ := st.Get("A")
	if task.Status == domain.TaskAccepted {
		t.Fatal("acceptance ran past the review budget")
	}
	if !strings.Contains(task.LastError, "budget") {
		t.Fatalf("last error = %q", task.LastError)
	}
}

// A clean targeted re-review must not erase the acceptance verdict for the same
// artifact version.
func TestAcceptanceBlockSurvivesTargetedReReview(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"] = "same"
	reviewer := &scriptedReviewer{responses: [][]domain.Finding{nil, nil}}
	judge := &scriptedReviewer{responses: [][]domain.Finding{
		{{Severity: domain.SeverityP1, Code: "missing_rows", Message: "only one row"}},
		{{Severity: domain.SeverityP1, Code: "missing_rows", Message: "only one row"}},
	}}
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	accept := func(string, map[domain.TaskID]string) error { return nil }
	e := engine.New(st, fake, 1, map[string]engine.ValidatorFunc{"ok": accept}).
		WithReviewer(reviewer).WithAcceptance(judge)
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "ok", ArtifactPath: "a.txt", MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 40); err != nil {
		t.Fatalf("run: %v", err)
	}
	task, _, _ := st.Get("A")
	if task.Status == domain.TaskAccepted {
		t.Fatal("a clean targeted review erased the acceptance block")
	}
	reviews, _ := st.ReviewsFor("A")
	var targeted, acceptance bool
	for _, r := range reviews {
		if r.Kind == domain.ReviewTargeted && !r.Blocking() {
			targeted = true
		}
		if r.Kind == domain.ReviewAcceptance && r.Blocking() {
			acceptance = true
		}
	}
	if !targeted || !acceptance {
		t.Fatalf("expected a clean targeted review and a blocking acceptance, got %+v", reviews)
	}
}

// The file fallback is what keeps quarantine across restarts when the database
// write failed.
func TestQuarantineFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, exec.NewFake(1), 1, nil).WithStateDir(dir)
	if err := os.WriteFile(filepath.Join(dir, "QUARANTINE"), []byte("stuck"), 0o644); err != nil {
		t.Fatal(err)
	}
	if reason := e.RestoreQuarantine(); reason != "stuck" {
		t.Fatalf("restored %q", reason)
	}
	if err := e.ClearQuarantine(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "QUARANTINE")); !os.IsNotExist(err) {
		t.Fatal("quarantine file was not removed")
	}
}
