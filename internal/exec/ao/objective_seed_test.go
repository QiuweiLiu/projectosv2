package ao

import (
	"context"
	"testing"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/engine"
	"projectos/kernel/internal/exec"
)

type seedRecordingBackend struct {
	start exec.StartRequest
}

func (b *seedRecordingBackend) Name() string { return "seed-recorder" }
func (b *seedRecordingBackend) Start(_ context.Context, req exec.StartRequest) (exec.StartResult, error) {
	b.start = req
	return exec.StartResult{ExternalID: "run"}, nil
}
func (b *seedRecordingBackend) Observe(context.Context, string) (exec.State, error) {
	return exec.State{Status: domain.RunSucceeded}, nil
}
func (b *seedRecordingBackend) Cancel(context.Context, string) error { return nil }
func (b *seedRecordingBackend) Collect(context.Context, string, string) (exec.Artifact, error) {
	return exec.Artifact{Result: `{"findings":[]}`}, nil
}

func TestObjectiveJudgeSeedsDeliveredFiles(t *testing.T) {
	backend := &seedRecordingBackend{}
	judge := NewAcceptance(backend)
	judge.Timeout = time.Second
	judge.Poll = time.Millisecond
	outcome, err := judge.JudgeObjective(context.Background(), engine.ObjectiveReviewRequest{
		ObjectiveID: "obj-1", Goal: "deliver calc.py",
		TaskResults: map[string]string{"calc.py": "def add(a, b):\n    return a + b\n"},
		TaskSummary: []string{"T1 -> calc.py"},
	})
	if err != nil || len(outcome.Findings) != 0 {
		t.Fatalf("judge = %+v %v", outcome, err)
	}
	if backend.start.Files["calc.py"] == "" {
		t.Fatalf("objective session did not receive delivered files: %+v", backend.start.Files)
	}
}
