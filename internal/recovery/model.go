package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/exec"
)

// Suggestion is a model's classification only. It never carries executable
// authority: Decide validates the category and still applies the Kernel policy.
type Suggestion struct {
	Category string `json:"category"`
	Reason   string `json:"reason,omitempty"`
}

// Model performs a single bounded classification session. It is optional; the
// deterministic diagnosis remains the default and the fallback.
type Model struct {
	Backend exec.Backend
	Timeout time.Duration
	Poll    time.Duration
}

var suggestedCategories = map[string]bool{
	"artifact_mismatch":       true,
	"review_blocked":          true,
	"dependency_failure":      true,
	"provider_transient":      true,
	"environment_failure":     true,
	"permission_denied":       true,
	"resource_exhaustion":     true,
	"plan_invalid":            true,
	"unknown_execution_state": true,
	"unexpected_side_effect":  true,
}

func (m *Model) Diagnose(ctx context.Context, evidence string) (Suggestion, error) {
	if m.Backend == nil {
		return Suggestion{}, fmt.Errorf("recovery diagnosis has no backend")
	}
	timeout, poll := m.Timeout, m.Poll
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	if poll <= 0 {
		poll = time.Second
	}
	prompt := `You classify one failed task for a recovery controller. Reply with JSON only:
{"category":"artifact_mismatch|review_blocked|dependency_failure|provider_transient|environment_failure|permission_denied|resource_exhaustion|plan_invalid|unknown_execution_state|unexpected_side_effect","reason":"short evidence-based reason"}

Rules:
- Use only the recorded evidence below; do not invent a cause.
- unknown_execution_state means the side effect may already have happened.
- unexpected_side_effect means the worker did something outside its declared artifact.
- The Kernel, not you, decides whether recovery is allowed.

<<<EVIDENCE
` + evidence + `
EVIDENCE>>>`
	sum := sha256.Sum256([]byte(evidence))
	key := hex.EncodeToString(sum[:8])
	start, err := m.Backend.Start(ctx, exec.StartRequest{
		TaskID: domain.TaskID("recovery-diagnosis"), Attempt: 1, Key: key,
		Prompt: prompt, ApprovalMode: "default",
	})
	if err != nil {
		return Suggestion{}, err
	}
	defer func() { _ = m.Backend.Cancel(context.Background(), start.ExternalID) }()
	deadline := time.Now().Add(timeout)
	for {
		state, err := m.Backend.Observe(ctx, start.ExternalID)
		if err != nil {
			return Suggestion{}, err
		}
		if state.Status != domain.RunRunning {
			if state.Status != domain.RunSucceeded {
				return Suggestion{}, fmt.Errorf("diagnosis session ended as %s", state.Status)
			}
			break
		}
		if time.Now().After(deadline) {
			return Suggestion{}, fmt.Errorf("diagnosis timed out after %s", timeout)
		}
		time.Sleep(poll)
	}
	artifact, err := m.Backend.Collect(ctx, start.ExternalID, "")
	if err != nil {
		return Suggestion{}, err
	}
	return parseSuggestion(artifact.Result)
}

func parseSuggestion(text string) (Suggestion, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return Suggestion{}, fmt.Errorf("diagnosis returned no JSON object")
	}
	var suggestion Suggestion
	if err := json.Unmarshal([]byte(text[start:end+1]), &suggestion); err != nil {
		return Suggestion{}, fmt.Errorf("diagnosis JSON: %w", err)
	}
	suggestion.Category = strings.TrimSpace(strings.ToLower(suggestion.Category))
	if !suggestedCategories[suggestion.Category] {
		return Suggestion{}, fmt.Errorf("diagnosis category %q is not allowed", suggestion.Category)
	}
	suggestion.Reason = strings.TrimSpace(suggestion.Reason)
	if len(suggestion.Reason) > 500 {
		suggestion.Reason = suggestion.Reason[:500]
	}
	return suggestion, nil
}
