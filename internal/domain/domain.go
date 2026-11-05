// Package domain holds the Kernel's task/run vocabulary. It has no I/O and no
// knowledge of any execution backend.
package domain

import (
	"time"

	"projectos/kernel/internal/verify"
)

type TaskID string

type TaskStatus string

const (
	TaskPending  TaskStatus = "pending"
	TaskRunning  TaskStatus = "running"
	TaskAccepted TaskStatus = "accepted"
	TaskRejected TaskStatus = "rejected"
	TaskBlocked  TaskStatus = "blocked"
	// TaskSuperseded means a newer plan version replaced this task before it
	// produced an accepted artifact. It is terminal but not a success.
	TaskSuperseded TaskStatus = "superseded"
)

type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
	RunUnknown   RunStatus = "unknown"
)

// Task is the durable unit of intent. Status is owned by the Kernel alone.
type Task struct {
	ID           TaskID
	Objective    string
	Prompt       string
	Validator    string
	ArtifactPath string
	Status       TaskStatus
	Attempts     int
	MaxAttempts  int
	LastError    string
	Deps         []TaskID
	ObjectiveID  string
	PlanVersion  int
	// Commands are executable acceptance checks (already allow-listed by verify).
	Commands []verify.Spec
}

// Run is one attempt at a Task. A new attempt always means a new Run.
type Run struct {
	ID         string
	TaskID     TaskID
	Attempt    int
	Backend    string
	Status     RunStatus
	Result     string
	Evidence   string
	ExternalID string
	StartedAt  time.Time
}

// Event is the append-only audit trail. It never replaces Task/Run state.
type Event struct {
	Seq    int64     `json:"seq"`
	TaskID TaskID    `json:"task_id"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail"`
	At     time.Time `json:"at"`
}
