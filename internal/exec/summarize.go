package exec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"projectos/kernel/internal/domain"
)

// Summarize runs one text-only session and returns its answer. It works with any
// Backend, so the same code powers AO and the CLI.
func Summarize(ctx context.Context, b Backend, prompt string) (string, error) {
	if b == nil {
		return "", fmt.Errorf("no backend")
	}
	sum := sha256.Sum256([]byte(prompt))
	start, err := b.Start(ctx, StartRequest{
		TaskID: domain.TaskID("reporter"), Attempt: 1, Key: hex.EncodeToString(sum[:8]),
		ApprovalMode: "default", Prompt: prompt,
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = b.Cancel(context.Background(), start.ExternalID) }()

	deadline := time.Now().Add(5 * time.Minute)
	for {
		state, oerr := b.Observe(ctx, start.ExternalID)
		if oerr != nil {
			return "", oerr
		}
		if state.Status != domain.RunRunning {
			if state.Status != domain.RunSucceeded {
				return "", fmt.Errorf("summary session ended as %s", state.Status)
			}
			break
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("summary timed out")
		}
		time.Sleep(2 * time.Second)
	}
	collected, err := b.Collect(ctx, start.ExternalID, "")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(collected.Result), nil
}
