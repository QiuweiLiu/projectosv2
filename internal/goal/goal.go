package goal

import (
	"fmt"
	"strings"
	"time"
)

type Status string

const (
	StatusDraft     Status = "draft"
	StatusPlanned   Status = "planned"
	StatusActive    Status = "active"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Objective is the durable, human-owned goal. It is deliberately separate from
// Task and from any Agent session.
type Objective struct {
	ID              string    `json:"id"`
	ProjectPath     string    `json:"projectPath"`
	Title           string    `json:"title"`
	Goal            string    `json:"goal"`
	Constraints     string    `json:"constraints,omitempty"`
	ExpectedOutcome string    `json:"expectedOutcome,omitempty"`
	Acceptance      string    `json:"acceptance,omitempty"`
	Status          Status    `json:"status"`
	PlanVersion     int       `json:"planVersion"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

var transitions = map[Status][]Status{
	StatusDraft:     {StatusPlanned, StatusCancelled},
	StatusPlanned:   {StatusActive, StatusDraft, StatusCancelled},
	StatusActive:    {StatusPaused, StatusCompleted, StatusFailed, StatusCancelled},
	StatusPaused:    {StatusActive, StatusCancelled},
	StatusCompleted: {},
	StatusFailed:    {StatusDraft, StatusCancelled},
	StatusCancelled: {},
}

func (s Status) CanMoveTo(next Status) bool {
	for _, allowed := range transitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

func (o Objective) Validate() error {
	if strings.TrimSpace(o.Goal) == "" {
		return fmt.Errorf("objective goal must not be empty")
	}
	if o.Status == "" {
		return fmt.Errorf("objective status must not be empty")
	}
	if len(o.Goal) > 8000 {
		return fmt.Errorf("objective goal is too long (%d chars)", len(o.Goal))
	}
	return nil
}
