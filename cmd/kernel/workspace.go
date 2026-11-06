package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/projectstate"
	"projectos/kernel/internal/store"
)

// workspace is the multi-objective layer above the kernel. The kernel still
// owns exactly one objective per state directory; a workspace keeps one state
// directory per objective under <root>/projects/<id>. This preserves the
// single-writer, one-scheduler invariants instead of teaching every store query
// about a second namespace.
type workspace struct {
	root string

	mu    sync.Mutex
	cache []project
	at    time.Time
}

// project is one objective plus the directory that owns it.
type project struct {
	ID        string
	ProjectID string
	Dir       string
	Obj       goal.Objective
}

// ProjectInfo is the lightweight project identity above objectives. It is kept
// in a workspace catalog; objective state stays in its own kernel.db.
type ProjectInfo struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

const defaultProjectID = "proj-default"

func newWorkspace(root string) *workspace { return &workspace{root: root} }

func (w *workspace) projectsDir() string { return filepath.Join(w.root, "projects") }

// projectDirs returns every directory that may hold a kernel database: the
// legacy single-objective root plus each project directory.
func (w *workspace) projectDirs() []string {
	var dirs []string
	if _, err := os.Stat(filepath.Join(w.root, "kernel.db")); err == nil {
		dirs = append(dirs, w.root)
	}
	entries, err := os.ReadDir(w.projectsDir())
	if err != nil {
		return dirs
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(w.projectsDir(), entry.Name())
		if _, err := os.Stat(filepath.Join(dir, "kernel.db")); err == nil {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// list reads one objective from each project directory. A broken project is
// skipped rather than taking the whole cockpit down; the caller can still open
// the remaining goals.
func (w *workspace) list() ([]project, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.cache) > 0 && time.Since(w.at) < 2*time.Second {
		return append([]project(nil), w.cache...), nil
	}
	out := make([]project, 0, 8)
	seen := map[string]bool{}
	for _, dir := range w.projectDirs() {
		meta := readProjectMeta(dir)
		st, err := store.OpenReadOnly(filepath.Join(dir, "kernel.db"))
		if err != nil {
			continue
		}
		objectives, err := st.ListObjectives()
		st.Close()
		if err != nil {
			continue
		}
		for _, obj := range objectives {
			if seen[obj.ID] {
				continue
			}
			seen[obj.ID] = true
			out = append(out, project{ID: obj.ID, ProjectID: meta, Dir: dir, Obj: obj})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Obj.UpdatedAt.Equal(out[j].Obj.UpdatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].Obj.UpdatedAt.After(out[j].Obj.UpdatedAt)
	})
	w.cache = append([]project(nil), out...)
	w.at = time.Now()
	return out, nil
}

// invalidate forces the next list to rescan. Creating an objective or changing
// its status calls this; a stale cockpit that silently hides a new goal is
// worse than a two-second rescan.
func (w *workspace) invalidate() {
	w.mu.Lock()
	w.cache = nil
	w.mu.Unlock()
}

// find resolves an objective id to its directory. The fast path matters because
// the cockpit polls events every three seconds.
func (w *workspace) find(id string) (project, bool, error) {
	id = strings.TrimSpace(id)
	if !validObjectiveID(id) {
		return project{}, false, nil
	}
	if dir := filepath.Join(w.projectsDir(), id); fileExists(filepath.Join(dir, "kernel.db")) {
		if obj, ok, err := readObjective(dir, id); err == nil && ok {
			return project{ID: id, ProjectID: readProjectMeta(dir), Dir: dir, Obj: obj}, true, nil
		}
	}
	all, err := w.list()
	if err != nil {
		return project{}, false, err
	}
	for _, p := range all {
		if p.ID == id {
			return p, true, nil
		}
	}
	return project{}, false, nil
}

// open returns a read-only handle plus the project. The caller closes the store.
func (w *workspace) open(id string) (*store.Store, project, error) {
	p, ok, err := w.find(id)
	if err != nil {
		return nil, project{}, err
	}
	if !ok {
		return nil, project{}, fmt.Errorf("unknown objective %s", id)
	}
	st, err := store.OpenReadOnly(filepath.Join(p.Dir, "kernel.db"))
	if err != nil {
		return nil, project{}, err
	}
	return st, p, nil
}

// resolve picks the objective named by ?objective=. With exactly one project the
// query is optional, which keeps old links and the single-goal CLI working.
func (w *workspace) resolve(id string) (project, bool, error) {
	if id != "" {
		return w.find(id)
	}
	all, err := w.list()
	if err != nil {
		return project{}, false, err
	}
	if len(all) == 1 {
		return all[0], true, nil
	}
	return project{}, false, nil
}

func (w *workspace) newProjectDir() (string, error) {
	dir := filepath.Join(w.projectsDir(), fmt.Sprintf("obj-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// adopt renames a provisional directory to the objective id the CLI generated.
// The directory name is cosmetic, but keeping it equal to the id makes find a
// single stat instead of a database scan.
func (w *workspace) adopt(dir, id string) string {
	target := filepath.Join(w.projectsDir(), id)
	if dir == target {
		return dir
	}
	if err := os.Rename(dir, target); err != nil {
		return dir
	}
	w.invalidate()
	return target
}

func validObjectiveID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\\`)
}

func readObjective(dir, id string) (goal.Objective, bool, error) {
	st, err := store.OpenReadOnly(filepath.Join(dir, "kernel.db"))
	if err != nil {
		return goal.Objective{}, false, err
	}
	defer st.Close()
	return st.GetObjective(id)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (w *workspace) catalogPath() string { return filepath.Join(w.root, "project-catalog.json") }

func readProjectMeta(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "project.json"))
	if err != nil {
		return defaultProjectID
	}
	var meta struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &meta) != nil || strings.TrimSpace(meta.ID) == "" {
		return defaultProjectID
	}
	return meta.ID
}

func writeProjectMeta(dir, id string) error {
	if strings.TrimSpace(id) == "" {
		id = defaultProjectID
	}
	raw, err := json.MarshalIndent(map[string]string{"id": id}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "project.json"), append(raw, '\n'), 0o644)
}

// Projects returns the catalog. The default project exists implicitly so an
// existing workspace does not need a migration before it can be used.
func (w *workspace) Projects() ([]ProjectInfo, error) {
	raw, err := os.ReadFile(w.catalogPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []ProjectInfo{{ID: defaultProjectID, Name: "默认项目"}}, nil
		}
		return nil, err
	}
	var cat struct {
		Projects []ProjectInfo `json:"projects"`
	}
	if err := json.Unmarshal(raw, &cat); err != nil {
		return nil, err
	}
	seen := false
	for _, p := range cat.Projects {
		if p.ID == defaultProjectID {
			seen = true
		}
	}
	if !seen {
		cat.Projects = append([]ProjectInfo{{ID: defaultProjectID, Name: "默认项目"}}, cat.Projects...)
	}
	return cat.Projects, nil
}

func (w *workspace) CreateProject(name string) (ProjectInfo, error) {
	return w.CreateProjectWithDescription(name, "")
}

func (w *workspace) CreateProjectWithDescription(name, description string) (ProjectInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ProjectInfo{}, fmt.Errorf("project name is required")
	}
	list, err := w.Projects()
	if err != nil {
		return ProjectInfo{}, err
	}
	info := ProjectInfo{ID: fmt.Sprintf("proj-%d", time.Now().UnixNano()), Name: name,
		Description: strings.TrimSpace(description), CreatedAt: time.Now().UTC()}
	list = append(list, info)
	raw, err := json.MarshalIndent(map[string]any{"projects": list}, "", "  ")
	if err != nil {
		return ProjectInfo{}, err
	}
	tmp := w.catalogPath() + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return ProjectInfo{}, err
	}
	if err := os.Rename(tmp, w.catalogPath()); err != nil {
		return ProjectInfo{}, err
	}
	_, _ = w.ProjectSnapshot(info.ID)
	return info, nil
}

// ProjectSnapshot rebuilds the project view from the objective databases and
// writes <root>/project-state/<id>/.project. The databases remain authoritative;
// this is a bounded, derived context file.
func (w *workspace) ProjectSnapshot(id string) (projectstate.ProjectSnapshot, error) {
	snap, err := w.BuildProjectSnapshot(id)
	if err != nil {
		return projectstate.ProjectSnapshot{}, err
	}
	if err := projectstate.WriteProject(w.root, snap); err != nil {
		return projectstate.ProjectSnapshot{}, err
	}
	return snap, nil
}

// BuildProjectSnapshot is the read-only form: it rebuilds facts without touching
// project-state. GET endpoints use it so an unauthenticated read cannot write.
func (w *workspace) BuildProjectSnapshot(id string) (projectstate.ProjectSnapshot, error) {
	info, ok, err := w.FindProject(id)
	if err != nil {
		return projectstate.ProjectSnapshot{}, err
	}
	if !ok {
		return projectstate.ProjectSnapshot{}, fmt.Errorf("unknown project %s", id)
	}
	w.invalidate()
	all, err := w.list()
	if err != nil {
		return projectstate.ProjectSnapshot{}, err
	}
	var objectives []projectstate.ProjectObjective
	var verified []string
	for _, p := range all {
		if p.ProjectID != id {
			continue
		}
		row := projectstate.ProjectObjective{ID: p.Obj.ID, Title: p.Obj.Title, Goal: p.Obj.Goal,
			Status: string(p.Obj.Status), PlanVersion: p.Obj.PlanVersion, UpdatedAt: p.Obj.UpdatedAt,
			Delivery: deliveryPresent(p.Dir)}
		st, err := store.OpenReadOnly(filepath.Join(p.Dir, "kernel.db"))
		if err != nil {
			continue
		}
		tasks, _ := st.List()
		for _, t := range tasks {
			if t.ObjectiveID != p.Obj.ID {
				continue
			}
			row.Total++
			if t.Status == domain.TaskAccepted {
				row.Accepted++
				verified = append(verified, p.Obj.ID+"\x00"+string(t.ID)+"\x00"+t.ArtifactPath)
			}
			if t.Status == domain.TaskBlocked || t.Status == domain.TaskRejected {
				reason := t.LastError
				if reason == "" {
					reason = string(t.Status)
				}
				row.Blockers = append(row.Blockers, string(t.ID)+": "+reason)
			}
		}
		if p.Obj.Status == goal.StatusFailed {
			if reviews, err := st.ReviewsFor(domain.TaskID("objective:" + p.Obj.ID)); err == nil {
				for i := len(reviews) - 1; i >= 0; i-- {
					if reviews[i].Kind == domain.ReviewObjective && reviews[i].Blocking() {
						for _, f := range reviews[i].Findings {
							if f.Severity == domain.SeverityP0 || f.Severity == domain.SeverityP1 {
								row.Blockers = append(row.Blockers, "objective acceptance: "+f.Message)
							}
						}
						break
					}
				}
			}
		}
		st.Close()
		objectives = append(objectives, row)
	}
	snap := projectstate.BuildProject(projectstate.ProjectMeta{ID: info.ID, Name: info.Name, CreatedAt: info.CreatedAt}, objectives, w.projectDecisions(id))
	for _, v := range verified {
		parts := strings.SplitN(v, "\x00", 3)
		if len(parts) == 3 {
			snap.AddVerified(parts[0], parts[1], parts[2])
		}
	}
	return snap, nil
}

func (w *workspace) projectDecisions(id string) []string {
	raw, err := os.ReadFile(filepath.Join(w.root, "project-state", id, "decisions.json"))
	if err != nil {
		return nil
	}
	var out []string
	_ = json.Unmarshal(raw, &out)
	return out
}

func (w *workspace) appendProjectDecision(id, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("decision is required")
	}
	if len([]rune(text)) > 500 {
		text = string([]rune(text)[:500]) + "…"
	}
	list := w.projectDecisions(id)
	list = append(list, text)
	if len(list) > 20 {
		list = list[len(list)-20:]
	}
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(w.root, "project-state", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "decisions.json.tmp")
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "decisions.json"))
}

func (w *workspace) FindProject(id string) (ProjectInfo, bool, error) {
	list, err := w.Projects()
	if err != nil {
		return ProjectInfo{}, false, err
	}
	for _, p := range list {
		if p.ID == id {
			return p, true, nil
		}
	}
	return ProjectInfo{}, false, nil
}
