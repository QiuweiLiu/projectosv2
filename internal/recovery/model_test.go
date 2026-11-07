package recovery

import (
	"context"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/goal"
)

func TestModelDiagnoseParsesBoundedCategory(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output[domain.TaskID("recovery-diagnosis")] = `{"category":"artifact_mismatch","reason":"missing symbol"}`
	model := &Model{Backend: fake, Timeout: 2e9, Poll: 1e6}
	got, err := model.Diagnose(context.Background(), "verification: missing DONE")
	if err != nil {
		t.Fatal(err)
	}
	if got.Category != "artifact_mismatch" {
		t.Fatalf("suggestion = %+v", got)
	}
}

func TestModelDiagnoseRejectsUnknownCategory(t *testing.T) {
	if _, err := parseSuggestion(`{"category":"do_whatever"}`); err == nil {
		t.Fatal("invalid category was accepted")
	}
}

func TestSuggestionCannotAuthorizeUnknownExecution(t *testing.T) {
	st, obj := fixture(t, goal.StatusActive, domain.Task{ID: "T1"})
	blockTask(t, st, "worker failed")
	suggestion := &Suggestion{Category: "unknown_execution_state", Reason: "might be side effect"}
	d, err := DecideWithSuggestion(st, obj, false, Policy{MaxRecoveries: 2, AttemptCap: 3}, suggestion)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != "stop" || d.Category != "unknown_execution_state" || !d.ModelAssisted {
		t.Fatalf("decision = %+v", d)
	}
}

func TestSuggestionCanClassifyUnknownIntoRetry(t *testing.T) {
	st, obj := fixture(t, goal.StatusFailed, domain.Task{ID: "T1"})
	blockTask(t, st, "worker failed")
	suggestion := &Suggestion{Category: "artifact_mismatch", Reason: "validator mismatch"}
	d, err := DecideWithSuggestion(st, obj, false, Policy{MaxRecoveries: 2, AttemptCap: 3}, suggestion)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != "retry" || d.Category != "artifact_mismatch" || !d.ModelAssisted {
		t.Fatalf("decision = %+v", d)
	}
}

func TestSuggestionDoesNotOverrideKnownCategory(t *testing.T) {
	st, obj := fixture(t, goal.StatusActive, domain.Task{ID: "T1"})
	blockTask(t, st, "permission denied")
	suggestion := &Suggestion{Category: "artifact_mismatch"}
	d, err := DecideWithSuggestion(st, obj, false, Policy{MaxRecoveries: 2, AttemptCap: 3}, suggestion)
	if err != nil {
		t.Fatal(err)
	}
	if d.Category != "permission_denied" || d.Action != "approve" {
		t.Fatalf("decision = %+v", d)
	}
}
