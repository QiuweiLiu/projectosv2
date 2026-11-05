package main

import (
	"net/http"
	"path/filepath"
	"time"

	"projectos/kernel/internal/store"
)

type taskRow struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Attempts int    `json:"attempts"`
	Evidence int    `json:"evidence"`
	Reviews  int    `json:"reviews"`
	Blocking int    `json:"blocking"`
	Error    string `json:"error,omitempty"`
}

type statusView struct {
	ObjectiveName string    `json:"name"`
	ObjectiveHash string    `json:"hash"`
	Pack          string    `json:"pack"`
	Quarantine    string    `json:"quarantine,omitempty"`
	Control       bool      `json:"control"`
	Instance      string    `json:"instance"`
	PromptChars   int       `json:"promptChars"`
	OutputChars   int       `json:"outputChars"`
	PromptTokens  int       `json:"promptTokens"`
	OutputTokens  int       `json:"outputTokens"`
	Estimated     bool      `json:"usageEstimated"`
	Tasks         []taskRow `json:"tasks"`
	At            time.Time `json:"at"`
}

func collectStatus(st *store.Store) (statusView, error) {
	var view statusView
	var err error
	if view.ObjectiveName, err = st.GetMeta("objective_name"); err != nil {
		return view, err
	}
	if view.ObjectiveHash, err = st.GetMeta("objective_hash"); err != nil {
		return view, err
	}
	if view.Pack, err = st.GetMeta("policy_pack"); err != nil {
		return view, err
	}
	if view.Quarantine, err = st.GetMeta("quarantine"); err != nil {
		return view, err
	}
	if view.PromptChars, view.OutputChars, err = st.UsageChars(); err != nil {
		return view, err
	}
	if view.PromptTokens, view.OutputTokens, view.Estimated, err = st.UsageTokens(); err != nil {
		return view, err
	}
	tasks, err := st.List()
	if err != nil {
		return view, err
	}
	for _, t := range tasks {
		ev, err := st.EvidenceFor(t.ID)
		if err != nil {
			return view, err
		}
		rv, err := st.ReviewsFor(t.ID)
		if err != nil {
			return view, err
		}
		blocking := 0
		for _, r := range rv {
			if r.Blocking() {
				blocking++
			}
		}
		view.Tasks = append(view.Tasks, taskRow{ID: string(t.ID), Status: string(t.Status),
			Attempts: t.Attempts, Evidence: len(ev), Reviews: len(rv), Blocking: blocking, Error: t.LastError})
	}
	view.Control = controlOn
	view.At = time.Now().UTC()
	return view, nil
}

func serveStatus(w http.ResponseWriter, st *store.Store) {
	view, err := collectStatus(st)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	view.Instance = serveInstance
	w.Header().Set("Content-Type", "application/json")
	_ = writeJSON(w, view)
}

// serveWorkspaceStatus keeps the legacy dashboard endpoint alive. The cockpit
// only needs the control flag and instance id; the objective list and task
// counts have their own scoped endpoints.
func serveWorkspaceStatus(w http.ResponseWriter, ws *workspace) {
	view := statusView{Control: controlOn, Instance: serveInstance, At: time.Now().UTC()}
	if st, err := store.OpenReadOnly(filepath.Join(ws.root, "kernel.db")); err == nil {
		if root, err := collectStatus(st); err == nil {
			view = root
			view.Control = controlOn
			view.Instance = serveInstance
			view.At = time.Now().UTC()
		}
		st.Close()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = writeJSON(w, view)
}
