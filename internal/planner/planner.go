// Package planner turns a natural-language goal into a structured plan. The
// Planner only proposes; the Kernel validates and a human confirms.
package planner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"projectos/kernel/internal/goal"
)

type Request struct {
	Goal        string
	ProjectPath string
	Constraints string
	Expected    string
	MaxTasks    int
	Feedback    string // validation error from the previous attempt
	Context     string // bounded project context; data, not instructions
}

type Result struct {
	Plan           goal.Plan
	Raw            string
	PromptChars    int
	OutputChars    int
	PromptTokens   int
	OutputTokens   int
	UsageEstimated bool
}

type Planner interface {
	Name() string
	Plan(ctx context.Context, req Request) (Result, error)
}

// Generate calls the planner with a bounded retry loop. Every attempt must pass
// Validate; free-form text never becomes a plan.
func Generate(ctx context.Context, p Planner, req Request, attempts int) (goal.Plan, Result, error) {
	if attempts <= 0 {
		attempts = 2
	}
	// Keep every attempt error. Overwriting with the last one hides the real
	// rejection behind an exhausted planner or a provider retry.
	var errs []error
	var usage Result
	for i := 0; i < attempts; i++ {
		result, err := p.Plan(ctx, req)
		usage.PromptChars += result.PromptChars
		usage.OutputChars += result.OutputChars
		usage.PromptTokens += result.PromptTokens
		usage.OutputTokens += result.OutputTokens
		usage.UsageEstimated = usage.UsageEstimated || result.UsageEstimated
		if err != nil {
			errs = append(errs, fmt.Errorf("attempt %d: %w", i+1, err))
			req.Feedback = "previous attempt failed: " + err.Error()
			continue
		}
		if err := result.Plan.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("attempt %d: %w", i+1, err))
			req.Feedback = "previous plan was rejected: " + err.Error()
			continue
		}
		if err := goal.ValidateImplementationChecks(result.Plan); err != nil {
			errs = append(errs, fmt.Errorf("attempt %d: %w", i+1, err))
			req.Feedback = "previous plan was rejected: " + err.Error()
			continue
		}
		result.PromptChars = usage.PromptChars
		result.OutputChars = usage.OutputChars
		result.PromptTokens = usage.PromptTokens
		result.OutputTokens = usage.OutputTokens
		result.UsageEstimated = usage.UsageEstimated
		return result.Plan, result, nil
	}
	return goal.Plan{}, Result{}, fmt.Errorf("planner failed after %d attempts: %w", attempts, errors.Join(errs...))
}

// Hash returns a stable identity for a plan.
func Hash(p goal.Plan) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// ExtractJSON pulls the first JSON object out of a model reply.
func ExtractJSON(text string) (string, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return "", fmt.Errorf("no JSON object in planner reply")
	}
	return text[start : end+1], nil
}

func Parse(text string) (goal.Plan, error) {
	raw, err := ExtractJSON(text)
	if err != nil {
		return goal.Plan{}, err
	}
	var plan goal.Plan
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		return goal.Plan{}, fmt.Errorf("parse plan: %w", err)
	}
	return plan, nil
}
