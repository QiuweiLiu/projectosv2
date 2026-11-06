package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

func seedProject(t *testing.T, dir, id string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	obj := goal.Objective{ID: id, ProjectPath: dir, Goal: "goal " + id, Status: goal.StatusActive,
		PlanVersion: 1, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
}

// A workspace must see one objective from each project directory and nothing
// from the empty root database that `serve` creates for compatibility.
func TestWorkspaceListsMultipleProjects(t *testing.T) {
	root := t.TempDir()
	seedProject(t, filepath.Join(root, "projects", "obj-a"), "obj-a")
	seedProject(t, filepath.Join(root, "projects", "obj-b"), "obj-b")
	seedProject(t, root, "obj-root")

	ws := newWorkspace(root)
	all, err := ws.list()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, p := range all {
		seen[p.ID] = p.Dir
	}
	if len(seen) != 3 {
		t.Fatalf("list = %+v, want 3", all)
	}
	for id, dir := range map[string]string{
		"obj-a":    filepath.Join(root, "projects", "obj-a"),
		"obj-b":    filepath.Join(root, "projects", "obj-b"),
		"obj-root": root,
	} {
		if seen[id] != dir {
			t.Fatalf("%s -> %q, want %q", id, seen[id], dir)
		}
	}
}

func TestWorkspaceFindAndResolve(t *testing.T) {
	root := t.TempDir()
	seedProject(t, filepath.Join(root, "projects", "obj-a"), "obj-a")
	seedProject(t, filepath.Join(root, "projects", "obj-b"), "obj-b")
	ws := newWorkspace(root)

	p, ok, err := ws.find("obj-b")
	if err != nil || !ok || p.Dir != filepath.Join(root, "projects", "obj-b") {
		t.Fatalf("find = %+v %v %v", p, ok, err)
	}
	if _, ok, err := ws.find("missing"); err != nil || ok {
		t.Fatalf("missing objective resolved: %v %v", ok, err)
	}
	// With more than one objective the query parameter is mandatory, otherwise
	// the server would silently pick a goal the user did not select.
	if _, ok, err := ws.resolve(""); err != nil || ok {
		t.Fatalf("ambiguous resolve = %v %v", ok, err)
	}
	if p, ok, _ := ws.resolve("obj-a"); !ok || p.ID != "obj-a" {
		t.Fatalf("resolve obj-a = %+v %v", p, ok)
	}
}

func TestWorkspaceResolveSingleProjectWithoutQuery(t *testing.T) {
	root := t.TempDir()
	seedProject(t, filepath.Join(root, "projects", "obj-only"), "obj-only")
	ws := newWorkspace(root)
	p, ok, err := ws.resolve("")
	if err != nil || !ok || p.ID != "obj-only" {
		t.Fatalf("resolve = %+v %v %v", p, ok, err)
	}
}

func TestWorkspaceAdoptMovesProvisionalDir(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	dir, err := ws.newProjectDir()
	if err != nil {
		t.Fatal(err)
	}
	seedProject(t, dir, "obj-real")
	moved := ws.adopt(dir, "obj-real")
	if moved != filepath.Join(root, "projects", "obj-real") {
		t.Fatalf("adopt -> %q", moved)
	}
	if _, err := os.Stat(filepath.Join(moved, "kernel.db")); err != nil {
		t.Fatal(err)
	}
}

// The list endpoint is the cockpit's only view of the workspace, so it must
// aggregate rather than report the empty root database.
func TestServeObjectiveListAggregatesWorkspace(t *testing.T) {
	root := t.TempDir()
	seedProject(t, filepath.Join(root, "projects", "obj-a"), "obj-a")
	seedProject(t, filepath.Join(root, "projects", "obj-b"), "obj-b")
	rec := httptest.NewRecorder()
	serveObjectiveList(rec, newWorkspace(root))
	var rows []objectiveSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	ids := map[string]bool{rows[0].ID: true, rows[1].ID: true}
	if !ids["obj-a"] || !ids["obj-b"] {
		t.Fatalf("ids = %+v", ids)
	}
}

func TestWorkspaceProjectCatalog(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	list, err := ws.Projects()
	if err != nil || len(list) != 1 || list[0].ID != defaultProjectID {
		t.Fatalf("default catalog = %+v %v", list, err)
	}
	p, err := ws.CreateProject("GPU Scheduling")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.Name != "GPU Scheduling" {
		t.Fatalf("project = %+v", p)
	}
	if _, ok, err := ws.FindProject(p.ID); err != nil || !ok {
		t.Fatal("created project not found", err)
	}
	dir := filepath.Join(root, "projects", "obj-a")
	seedProject(t, dir, "obj-a")
	if err := writeProjectMeta(dir, p.ID); err != nil {
		t.Fatal(err)
	}
	all, err := ws.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ProjectID != p.ID {
		t.Fatalf("objective project mapping = %+v", all)
	}
}
