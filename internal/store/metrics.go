package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Metrics is a read-only summary of one objective's Kernel state. It is derived
// from tasks/runs/reviews/plans and never from model prose.
type Metrics struct {
	Tasks              int `json:"tasks"`
	Accepted           int `json:"accepted"`
	FirstPass          int `json:"acceptedFirstAttempt"`
	AcceptedAfterRetry int `json:"acceptedAfterRetry"`
	Running            int `json:"running"`
	Pending            int `json:"pending"`
	Blocked            int `json:"blocked"`
	Attempts           int `json:"attempts"`
	Runs               int `json:"runs"`
	CompletedRuns      int `json:"completedRuns"`
	FailedRuns         int `json:"failedRuns"`
	CancelledRuns      int `json:"cancelledRuns"`
	UnknownRuns        int `json:"unknownRuns"`
	ActiveRuns         int `json:"activeRuns"`
	Reviews            int `json:"reviews"`
	BlockingReviews    int `json:"blockingReviews"`

	PromptTokens   int  `json:"promptTokens"`
	OutputTokens   int  `json:"outputTokens"`
	UsageEstimated bool `json:"usageEstimated"`

	PlannerPromptTokens    int `json:"plannerPromptTokens"`
	PlannerOutputTokens    int `json:"plannerOutputTokens"`
	WorkerPromptTokens     int `json:"workerPromptTokens"`
	WorkerOutputTokens     int `json:"workerOutputTokens"`
	ReviewerPromptTokens   int `json:"reviewerPromptTokens"`
	ReviewerOutputTokens   int `json:"reviewerOutputTokens"`
	AcceptancePromptTokens int `json:"acceptancePromptTokens"`
	AcceptanceOutputTokens int `json:"acceptanceOutputTokens"`
	ObjectivePromptTokens  int `json:"objectivePromptTokens"`
	ObjectiveOutputTokens  int `json:"objectiveOutputTokens"`
	PlanVersions           int `json:"planVersions"`
	RecoveryCount          int `json:"recoveryCount"`
	AutoRecoveries         int `json:"autoRecoveries"`
	ManualRecoveries       int `json:"manualRecoveries"`

	RunDurationMS     int64  `json:"runDurationMillis"`
	WallClockMS       int64  `json:"wallClockMillis"`
	StartedAt         string `json:"startedAt,omitempty"`
	LastRunFinishedAt string `json:"lastRunFinishedAt,omitempty"`
	FinishedAt        string `json:"finishedAt,omitempty"`
	ObjectiveStatus   string `json:"objectiveStatus,omitempty"`
}

// Metrics returns stable values for every legal lifecycle state: an empty
// objective, a planned objective with no runs, running/failed runs, and a
// finished objective. SQL NULL from MIN/MAX is never scanned into a Go string.
func (s *Store) Metrics() (Metrics, error) {
	var m Metrics
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(attempts),0),
		COALESCE(SUM(CASE WHEN status='accepted' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='accepted' AND attempts=1 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='accepted' AND attempts>1 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='running' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='pending' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status IN ('blocked','rejected') THEN 1 ELSE 0 END),0)
		FROM tasks`).Scan(&m.Tasks, &m.Attempts, &m.Accepted, &m.FirstPass, &m.AcceptedAfterRetry,
		&m.Running, &m.Pending, &m.Blocked); err != nil {
		return m, err
	}

	var minStart, maxFinish sql.NullString
	if err := s.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN status='succeeded' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='cancelled' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='unknown' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='running' THEN 1 ELSE 0 END),0),
		MIN(started_at), MAX(finished_at)
		FROM runs`).Scan(&m.Runs, &m.CompletedRuns, &m.FailedRuns, &m.CancelledRuns,
		&m.UnknownRuns, &m.ActiveRuns, &minStart, &maxFinish); err != nil {
		return m, err
	}
	if minStart.Valid {
		m.StartedAt = minStart.String
	}
	if maxFinish.Valid {
		m.LastRunFinishedAt = maxFinish.String
		// An objective with an in-flight run is not finished. Keeping FinishedAt
		// empty stops callers from treating a partial run as a completed one.
		if m.ActiveRuns == 0 {
			m.FinishedAt = maxFinish.String
		}
	}

	rows, err := s.db.Query(`SELECT started_at, finished_at FROM runs WHERE finished_at IS NOT NULL`)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var started, finished string
		if err := rows.Scan(&started, &finished); err != nil {
			rows.Close()
			return m, err
		}
		a, aerr := time.Parse(time.RFC3339Nano, started)
		b, berr := time.Parse(time.RFC3339Nano, finished)
		if aerr == nil && berr == nil && b.After(a) {
			m.RunDurationMS += b.Sub(a).Milliseconds()
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return m, err
	}

	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(blocking),0) FROM reviews`).
		Scan(&m.Reviews, &m.BlockingReviews); err != nil {
		return m, err
	}

	planner, err := s.usageSum("plans", "")
	if err != nil {
		return m, err
	}
	worker, err := s.usageSum("runs", "")
	if err != nil {
		return m, err
	}
	reviewer, err := s.usageSum("reviews", "kind IN ('full','targeted')")
	if err != nil {
		return m, err
	}
	acceptance, err := s.usageSum("reviews", "kind='acceptance'")
	if err != nil {
		return m, err
	}
	objective, err := s.usageSum("reviews", "kind='objective'")
	if err != nil {
		return m, err
	}
	m.PlannerPromptTokens, m.PlannerOutputTokens = planner.prompt, planner.output
	m.WorkerPromptTokens, m.WorkerOutputTokens = worker.prompt, worker.output
	m.ReviewerPromptTokens, m.ReviewerOutputTokens = reviewer.prompt, reviewer.output
	m.AcceptancePromptTokens, m.AcceptanceOutputTokens = acceptance.prompt, acceptance.output
	m.ObjectivePromptTokens, m.ObjectiveOutputTokens = objective.prompt, objective.output
	m.PromptTokens = planner.prompt + worker.prompt + reviewer.prompt + acceptance.prompt + objective.prompt
	m.OutputTokens = planner.output + worker.output + reviewer.output + acceptance.output + objective.output
	m.UsageEstimated = planner.estimated || worker.estimated || reviewer.estimated || acceptance.estimated || objective.estimated

	if ok, err := s.hasTable("plans"); err != nil {
		return m, err
	} else if ok {
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM plans`).Scan(&m.PlanVersions); err != nil {
			return m, err
		}
	}
	if raw, err := s.GetMeta("recovery_count"); err == nil {
		_, _ = fmt.Sscanf(raw, "%d", &m.RecoveryCount)
	}
	if ok, err := s.hasTable("events"); err != nil {
		return m, err
	} else if ok {
		if err := s.db.QueryRow(`SELECT
			COALESCE(SUM(CASE WHEN kind='recovery_retry' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN kind='recovery_approved' THEN 1 ELSE 0 END),0)
			FROM events`).Scan(&m.AutoRecoveries, &m.ManualRecoveries); err != nil {
			return m, err
		}
	}

	// Wall clock is objective creation to now (active) or to the last transition
	// (terminal). It is deliberately separate from the sum of run durations:
	// parallel runs can exceed it and waiting/approval time is included here.
	var created, updated, status string
	hasObjectives, err := s.hasTable("objectives")
	if err != nil {
		return m, err
	}
	if hasObjectives {
		err = s.db.QueryRow(`SELECT created_at, updated_at, status FROM objectives
			ORDER BY created_at DESC LIMIT 1`).Scan(&created, &updated, &status)
		if err != nil && err != sql.ErrNoRows {
			return m, err
		}
	} else {
		err = sql.ErrNoRows
	}
	if err == nil {
		m.ObjectiveStatus = status
		// FinishedAt is the objective end, not merely the last run end. An
		// active/failed-but-recoverable objective still has an open lifecycle;
		// LastRunFinishedAt remains available for its last completed attempt.
		if status != "completed" && status != "failed" && status != "cancelled" {
			m.FinishedAt = ""
		}
		start, serr := time.Parse(time.RFC3339Nano, created)
		end := time.Now().UTC()
		switch status {
		case "completed", "failed", "cancelled":
			if parsed, perr := time.Parse(time.RFC3339Nano, updated); perr == nil {
				end = parsed
			}
		}
		if serr == nil && end.After(start) {
			m.WallClockMS = end.Sub(start).Milliseconds()
		}
	}
	return m, nil
}

type usageTotals struct {
	prompt, output int
	estimated      bool
}

// usageSum applies the same fallback rule as UsageTokens: real tokens when
// present, otherwise prompt_chars/4. A zero-token row is marked estimated.
func (s *Store) usageSum(table, where string) (usageTotals, error) {
	hasTokens, err := s.hasColumn(table, "prompt_tokens")
	if err != nil {
		return usageTotals{}, err
	}
	hasChars, err := s.hasColumn(table, "prompt_chars")
	if err != nil {
		return usageTotals{}, err
	}
	if !hasChars && !hasTokens {
		return usageTotals{}, nil
	}
	if where != "" {
		where = " WHERE " + where
	}
	var query string
	if hasTokens {
		query = `SELECT
			COALESCE(SUM(CASE WHEN prompt_tokens > 0 THEN prompt_tokens ELSE prompt_chars/4 END),0),
			COALESCE(SUM(CASE WHEN output_tokens > 0 THEN output_tokens ELSE output_chars/4 END),0),
			COALESCE(MAX(CASE WHEN usage_estimated = 1 OR prompt_tokens = 0 THEN 1 ELSE 0 END),0)
			FROM ` + table + where
	} else {
		query = `SELECT COALESCE(SUM(prompt_chars/4),0), COALESCE(SUM(output_chars/4),0), 1
			FROM ` + table + where
	}
	var p, o, e int
	if err := s.db.QueryRow(query).Scan(&p, &o, &e); err != nil {
		return usageTotals{}, err
	}
	return usageTotals{prompt: p, output: o, estimated: e == 1}, nil
}
