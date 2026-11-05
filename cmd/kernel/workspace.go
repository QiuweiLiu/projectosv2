package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"projectos/kernel/internal/goal"
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
	ID  string
	Dir string
	Obj goal.Objective
}

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
			out = append(out, project{ID: obj.ID, Dir: dir, Obj: obj})
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
			return project{ID: id, Dir: dir, Obj: obj}, true, nil
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
