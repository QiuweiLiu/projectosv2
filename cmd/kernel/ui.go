package main

import (
	_ "embed"
	"net/http"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/report"
	"projectos/kernel/internal/store"
)

// The cockpit is a static file so it can be edited without touching Go.
//
//go:embed ui.html
var dashboardHTML []byte

func serveDashboard(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(dashboardHTML)
}

type evidenceRow struct {
	Accepted bool      `json:"accepted"`
	Version  string    `json:"version"`
	Digest   string    `json:"digest"`
	Reason   string    `json:"reason,omitempty"`
	At       time.Time `json:"at"`
}

type reviewRow struct {
	Kind        string           `json:"kind"`
	Version     string           `json:"version"`
	Blocking    bool             `json:"blocking"`
	Findings    []domain.Finding `json:"findings"`
	PromptChars int              `json:"promptChars"`
	OutputChars int              `json:"outputChars"`
	At          time.Time        `json:"at"`
}

type taskDetail struct {
	ID          string        `json:"id"`
	Status      string        `json:"status"`
	Attempts    int           `json:"attempts"`
	Artifact    string        `json:"artifact"`
	Deps        []string      `json:"deps"`
	Error       string        `json:"error,omitempty"`
	Evidence    []evidenceRow `json:"evidence"`
	Reviews     []reviewRow   `json:"reviews"`
	Explanation string        `json:"explanation,omitempty"`
}

func serveTask(w http.ResponseWriter, st *store.Store, dir string, id domain.TaskID) {
	task, ok, err := st.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "unknown task", http.StatusNotFound)
		return
	}
	detail := taskDetail{ID: string(task.ID), Status: string(task.Status), Attempts: task.Attempts,
		Artifact: task.ArtifactPath, Error: task.LastError}
	for _, d := range task.Deps {
		detail.Deps = append(detail.Deps, string(d))
	}
	evidence, err := st.EvidenceFor(task.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, e := range evidence {
		detail.Evidence = append(detail.Evidence, evidenceRow{Accepted: e.Accepted, Version: e.ArtifactVersion,
			Digest: e.Digest, Reason: e.Reason, At: e.At})
	}
	reviews, err := st.ReviewsFor(task.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, r := range reviews {
		detail.Reviews = append(detail.Reviews, reviewRow{Kind: string(r.Kind), Version: r.ArtifactVersion,
			Blocking: r.Blocking(), Findings: r.Findings, PromptChars: r.PromptChars, OutputChars: r.OutputChars, At: r.At})
	}
	// A cached Chinese explanation is only valid for the findings it was written
	// from; the hash makes stale text disappear instead of lying.
	hash := report.FindingsHash(collectFindings(reviews))
	if text, ok := report.ReadExplanation(dir, task.ID, hash); ok {
		detail.Explanation = text
	}
	w.Header().Set("Content-Type", "application/json")
	_ = writeJSON(w, detail)
}

func serveEvents(w http.ResponseWriter, st *store.Store, after int64, limit int, tail bool) {
	var events []domain.Event
	var err error
	if tail {
		events, err = st.EventsTail(limit)
	} else {
		events, err = st.EventsPage(after, limit)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if events == nil {
		// A client that parses `events` should get [] rather than null when the
		// cursor is already at the end of the log.
		events = []domain.Event{}
	}
	next := after
	for _, event := range events {
		if event.Seq > next {
			next = event.Seq
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = writeJSON(w, map[string]any{"events": events, "next": next})
}
