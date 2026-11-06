package store

import (
	"time"
)

type Metrics struct {
	Tasks           int    `json:"tasks"`
	Accepted        int    `json:"accepted"`
	Running         int    `json:"running"`
	Blocked         int    `json:"blocked"`
	Attempts        int    `json:"attempts"`
	Runs            int    `json:"runs"`
	FailedRuns      int    `json:"failedRuns"`
	Reviews         int    `json:"reviews"`
	BlockingReviews int    `json:"blockingReviews"`
	PromptTokens    int    `json:"promptTokens"`
	OutputTokens    int    `json:"outputTokens"`
	UsageEstimated  bool   `json:"usageEstimated"`
	RunDurationMS   int64  `json:"runDurationMillis"`
	StartedAt       string `json:"startedAt,omitempty"`
	FinishedAt      string `json:"finishedAt,omitempty"`
}

func (s *Store) Metrics() (Metrics, error) {
	var m Metrics
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(attempts),0),
		COALESCE(SUM(CASE WHEN status='accepted' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='running' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status IN ('blocked','rejected') THEN 1 ELSE 0 END),0)
		FROM tasks`).Scan(&m.Tasks, &m.Attempts, &m.Accepted, &m.Running, &m.Blocked); err != nil {
		return m, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0),
		MIN(started_at), MAX(finished_at)
		FROM runs`).Scan(&m.Runs, &m.FailedRuns, &m.StartedAt, &m.FinishedAt); err != nil {
		return m, err
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
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(blocking),0) FROM reviews`).
		Scan(&m.Reviews, &m.BlockingReviews); err != nil {
		return m, err
	}
	p, o, estimated, err := s.UsageTokens()
	if err != nil {
		return m, err
	}
	m.PromptTokens, m.OutputTokens, m.UsageEstimated = p, o, estimated
	return m, nil
}
