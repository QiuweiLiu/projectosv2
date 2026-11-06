package main

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"projectos/kernel/internal/delivery"
	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/modelcfg"
	"projectos/kernel/internal/progress"
	"projectos/kernel/internal/report"
	"projectos/kernel/internal/store"
)

// objectiveSummary is what a list view needs: one row per goal, counts derived
// from the currently confirmed plan. Counts come from task status, never from
// an agent's self-report.
type objectiveSummary struct {
	ID          string     `json:"id"`
	ProjectID   string     `json:"projectId,omitempty"`
	Title       string     `json:"title,omitempty"`
	Goal        string     `json:"goal"`
	Status      string     `json:"status"`
	PlanVersion int        `json:"planVersion"`
	Tasks       taskCounts `json:"tasks"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

type taskCounts struct {
	Total      int `json:"total"`
	Accepted   int `json:"accepted"`
	Running    int `json:"running"`
	Pending    int `json:"pending"`
	Blocked    int `json:"blocked"`
	Superseded int `json:"superseded"`
}

func countTasks(tasks []domain.Task) taskCounts {
	var counts taskCounts
	for _, t := range tasks {
		counts.Total++
		switch t.Status {
		case domain.TaskAccepted:
			counts.Accepted++
		case domain.TaskRunning:
			counts.Running++
		case domain.TaskPending:
			counts.Pending++
		case domain.TaskBlocked, domain.TaskRejected:
			counts.Blocked++
		case domain.TaskSuperseded:
			counts.Superseded++
		}
	}
	return counts
}

func summarise(st *store.Store, obj goal.Objective) (objectiveSummary, error) {
	tasks, err := st.TasksForPlan(obj.ID, obj.PlanVersion)
	if err != nil {
		return objectiveSummary{}, err
	}
	return objectiveSummary{
		ID: obj.ID, ProjectID: readProjectMeta(filepath.Dir(obj.ProjectPath)), Title: obj.Title,
		Goal: obj.Goal, Status: string(obj.Status), PlanVersion: obj.PlanVersion,
		Tasks: countTasks(tasks), UpdatedAt: obj.UpdatedAt,
	}, nil
}

func serveObjectiveList(w http.ResponseWriter, ws *workspace) {
	projects, err := ws.list()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]objectiveSummary, 0, len(projects))
	for _, p := range projects {
		st, err := store.OpenReadOnly(filepath.Join(p.Dir, "kernel.db"))
		if err != nil {
			continue
		}
		row, err := summarise(st, p.Obj)
		st.Close()
		if err != nil {
			continue
		}
		out = append(out, row)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = writeJSON(w, out)
}

type objectiveTaskRow struct {
	ID           string   `json:"id"`
	Status       string   `json:"status"`
	Attempts     int      `json:"attempts"`
	ArtifactPath string   `json:"artifactPath,omitempty"`
	Deps         []string `json:"deps,omitempty"`
	Error        string   `json:"error,omitempty"`
	Risk         string   `json:"risk,omitempty"`
	Commands     []string `json:"commands,omitempty"`
}

type objectiveDetail struct {
	Objective         goal.Objective     `json:"objective"`
	Plan              *goal.Plan         `json:"plan,omitempty"`
	PlanVersion       int                `json:"planVersion"`
	Plans             []planRow          `json:"plans,omitempty"`
	PlanHash          string             `json:"planHash,omitempty"`
	PlanStatus        string             `json:"planStatus,omitempty"`
	Tasks             []objectiveTaskRow `json:"tasks"`
	Counts            taskCounts         `json:"counts"`
	Delivery          bool               `json:"delivery"`
	CancelRequested   bool               `json:"cancelRequested"`
	PauseRequested    bool               `json:"pauseRequested"`
	RequiresApproval  bool               `json:"requiresApproval"`
	ApprovalReasons   []string           `json:"approvalReasons,omitempty"`
	ObjectiveFindings []domain.Finding   `json:"objectiveFindings,omitempty"`
}

type planRow struct {
	Version int    `json:"version"`
	Status  string `json:"status"`
	Hash    string `json:"hash,omitempty"`
}

func countTaskRows(rows []objectiveTaskRow) taskCounts {
	var counts taskCounts
	for _, row := range rows {
		counts.Total++
		switch domain.TaskStatus(row.Status) {
		case domain.TaskAccepted:
			counts.Accepted++
		case domain.TaskRunning:
			counts.Running++
		case domain.TaskPending:
			counts.Pending++
		case domain.TaskBlocked, domain.TaskRejected:
			counts.Blocked++
		case domain.TaskSuperseded:
			counts.Superseded++
		}
	}
	return counts
}

func serveObjectiveDetail(w http.ResponseWriter, st *store.Store, dir, id string) {
	obj, ok, err := st.GetObjective(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "unknown objective", http.StatusNotFound)
		return
	}
	detail := objectiveDetail{Objective: obj, Delivery: deliveryPresent(dir)}
	// A plan exists before it is confirmed, but PlanVersion only moves on
	// confirm. Fall back to the latest plan so the cockpit can show a proposal
	// that is still waiting for a human.
	record, ok, err := st.GetPlan(obj.ID, obj.PlanVersion)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		if latest, found, lerr := st.LatestPlan(obj.ID); lerr != nil {
			http.Error(w, lerr.Error(), http.StatusInternalServerError)
			return
		} else if found {
			record, ok = latest, true
		}
	}
	if ok {
		spec := record.Spec
		detail.Plan = &spec
		detail.PlanVersion = record.Version
		detail.PlanHash = record.Hash
		detail.PlanStatus = record.Status
	}
	tasks, err := st.TasksForPlan(obj.ID, record.Version)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Merge the plan with what actually exists: a continuation adds tasks to the
	// plan before they are compiled, and they must be visible immediately.
	byID := map[string]domain.Task{}
	for _, t := range tasks {
		byID[string(t.ID)] = t
	}
	if detail.Plan != nil {
		for _, spec := range detail.Plan.Tasks {
			if t, ok := byID[spec.ID]; ok {
				row := objectiveTaskRow{ID: string(t.ID), Status: string(t.Status), Attempts: t.Attempts,
					ArtifactPath: t.ArtifactPath, Error: t.LastError, Risk: spec.Risk}
				for _, v := range spec.Validators {
					if v.Type == "command" {
						row.Commands = append(row.Commands, v.Command)
					}
				}
				for _, d := range t.Deps {
					row.Deps = append(row.Deps, string(d))
				}
				detail.Tasks = append(detail.Tasks, row)
				delete(byID, spec.ID)
				continue
			}
			row := objectiveTaskRow{ID: spec.ID, Status: string(domain.TaskPending), Risk: spec.Risk}
			if len(spec.Artifacts) > 0 {
				row.ArtifactPath = spec.Artifacts[0]
			}
			row.Deps = append(row.Deps, spec.Deps...)
			for _, v := range spec.Validators {
				if v.Type == "command" {
					row.Commands = append(row.Commands, v.Command)
				}
			}
			detail.Tasks = append(detail.Tasks, row)
		}
	}
	for _, t := range tasks {
		if _, pending := byID[string(t.ID)]; !pending {
			continue
		}
		row := objectiveTaskRow{ID: string(t.ID), Status: string(t.Status), Attempts: t.Attempts,
			ArtifactPath: t.ArtifactPath, Error: t.LastError}
		for _, d := range t.Deps {
			row.Deps = append(row.Deps, string(d))
		}
		detail.Tasks = append(detail.Tasks, row)
	}
	// Superseded tasks are not part of the current plan, so count them separately
	// instead of implying nothing was replaced.
	detail.Counts = countTaskRows(detail.Tasks)
	if all, err := st.List(); err == nil {
		for _, t := range all {
			if t.ObjectiveID == obj.ID && t.Status == domain.TaskSuperseded {
				detail.Counts.Superseded++
			}
		}
	}
	detail.CancelRequested = cancelRequested(dir)
	detail.PauseRequested = pauseRequested(dir)
	if ok && len(record.Spec.Tasks) > 0 {
		detail.ApprovalReasons = approvalReasons(record.Spec)
		detail.RequiresApproval = len(detail.ApprovalReasons) > 0
	}
	if reviews, err := st.ReviewsFor(domain.TaskID("objective:" + obj.ID)); err == nil {
		for i := len(reviews) - 1; i >= 0; i-- {
			if reviews[i].Kind == domain.ReviewObjective {
				detail.ObjectiveFindings = reviews[i].Findings
				break
			}
		}
	}

	// History is shown, not hidden: after a replan the current plan alone makes
	// the objective look like it always had this shape.
	if plans, err := st.ListPlans(obj.ID); err == nil {
		for _, plan := range plans {
			detail.Plans = append(detail.Plans, planRow{Version: plan.Version, Status: plan.Status, Hash: plan.Hash})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = writeJSON(w, detail)
}

func serveMetrics(w http.ResponseWriter, st *store.Store) {
	m, err := st.Metrics()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = writeJSON(w, m)
}

func serveProgress(w http.ResponseWriter, st *store.Store, id string) {
	obj, ok, err := st.GetObjective(id)
	if err != nil || !ok {
		http.Error(w, "unknown objective", http.StatusNotFound)
		return
	}
	needs := false
	if plan, ok, err := st.GetPlan(obj.ID, obj.PlanVersion); err == nil && ok {
		needs = len(approvalReasons(plan.Spec)) > 0
	}
	r, err := progress.Assess(st, obj, needs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = writeJSON(w, r)
}

func deliveryPresent(dir string) bool {
	for _, name := range []string{"delivery.md", "delivery.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

type reportView struct {
	Text         string          `json:"text"`
	ModelText    string          `json:"modelText,omitempty"`
	ModelAt      time.Time       `json:"modelAt,omitempty"`
	GeneratedAt  time.Time       `json:"generatedAt"`
	Deliverables []delivery.File `json:"deliverables,omitempty"`
}

// serveReport returns the rule-based briefing, plus the last model-written one
// if it exists. It never runs a model itself.
func serveReport(w http.ResponseWriter, st *store.Store, dir string) {
	obj, ok, err := singleObjective(st)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		_ = writeJSON(w, reportView{Text: "这个状态目录还没有目标。", GeneratedAt: time.Now().UTC()})
		return
	}
	brief, err := report.Build(st, obj)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	view := reportView{Text: report.Render(brief), GeneratedAt: time.Now().UTC()}
	if files, err := delivery.List(dir, 20, 600); err == nil {
		view.Deliverables = files
	}
	tasks, _ := st.List()
	if text, ok := report.ReadModelReport(dir, report.StateHash(obj, tasks)); ok {
		view.ModelText = text
		if info, err := os.Stat(report.ModelReportPath(dir)); err == nil {
			view.ModelAt = info.ModTime().UTC()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = writeJSON(w, view)
}

type modelsView struct {
	Settings modelcfg.Settings `json:"settings"`
	Models   []string          `json:"models"`
	At       time.Time         `json:"at,omitempty"`
}

// serveModels exposes the per-role settings and the cached model list. It never
// shells out; refreshing is a separate POST.
func serveModels(w http.ResponseWriter, dir string) {
	cache := modelcfg.LoadCache(dir)
	w.Header().Set("Content-Type", "application/json")
	_ = writeJSON(w, modelsView{Settings: modelcfg.Load(dir), Models: cache.Models, At: cache.At})
}
