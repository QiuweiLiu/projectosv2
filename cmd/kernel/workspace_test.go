package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/projectstate"
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

func TestProjectSnapshotBuildsFromObjectiveDB(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	project, err := ws.CreateProject("GPU Scheduling")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "projects", "obj-a")
	seedProject(t, dir, "obj-a")
	if err := writeProjectMeta(dir, project.ID); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(domain.Task{ID: "T1", ObjectiveID: "obj-a", PlanVersion: 1,
		ArtifactPath: "a.txt", Validator: "nonempty"}); err != nil {
		t.Fatal(err)
	}
	run, ok, err := st.Claim("T1", "r1", "fake")
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(store.Settlement{RunID: run.ID, RunStatus: domain.RunSucceeded,
		Result: "done", Accepted: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveObjective("obj-a", goal.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := os.WriteFile(filepath.Join(dir, "delivery.json"),
		[]byte(`{"status":"completed","checks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "out", "a.txt"), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ws.appendProjectDecision(project.ID, "use baseline B"); err != nil {
		t.Fatal(err)
	}
	snap, err := ws.ProjectSnapshot(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Objectives) != 1 || snap.Objectives[0].Accepted != 1 || snap.Objectives[0].Total != 1 {
		t.Fatalf("objectives = %+v", snap.Objectives)
	}
	if len(snap.Verified) != 1 || !strings.Contains(snap.Verified[0], "T1 a.txt") {
		t.Fatalf("verified = %+v", snap.Verified)
	}
	if len(snap.Decisions) != 1 || snap.Decisions[0] != "use baseline B" {
		t.Fatalf("decisions = %+v", snap.Decisions)
	}
	shared, err := os.ReadFile(filepath.Join(root, "project-state", project.ID, "artifacts", "obj-a", "a.txt"))
	if err != nil || string(shared) != "done" {
		t.Fatalf("shared artifact = %q %v", shared, err)
	}
	manifest, err := readProjectManifest(root, project.ID)
	if err != nil || len(manifest.Entries) != 1 || manifest.Entries[0].ContentSHA256 == "" {
		t.Fatalf("manifest = %+v %v", manifest, err)
	}
	for _, name := range []string{"PROJECT.md", "STATE.md", "HANDOFF.md", "DECISIONS.md", "snapshot.json"} {
		if _, err := os.Stat(filepath.Join(projectstate.ProjectDir(root, project.ID), name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestProjectAPIExposesAndRefreshesSnapshot(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	mux, ctl, err := buildMux(ws, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(mux)
	defer ts.Close()
	project, err := ws.CreateProjectWithDescription("GPU Scheduling", "long-term context")
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.Get(ts.URL + "/api/projects/" + project.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("GET project = %d", res.StatusCode)
	}
	var view struct {
		Project  ProjectInfo                  `json:"project"`
		Snapshot projectstate.ProjectSnapshot `json:"snapshot"`
	}
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.Snapshot.ProjectID != project.ID || view.Snapshot.Name != "GPU Scheduling" {
		t.Fatalf("snapshot = %+v", view.Snapshot)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/"+project.ID+"/decisions",
		strings.NewReader(`{"text":"use baseline B"}`))
	req.Header.Set("X-Kernel-Token", ctl.token)
	req.Header.Set("Content-Type", "application/json")
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != 200 {
		t.Fatalf("decision = %d", res2.StatusCode)
	}
	raw, err := os.ReadFile(filepath.Join(projectstate.ProjectDir(root, project.ID), "DECISIONS.md"))
	if err != nil || !strings.Contains(string(raw), "use baseline B") {
		t.Fatalf("decisions = %s %v", raw, err)
	}
}

func TestSeedProjectArtifacts(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "projects", "obj-b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "project.json"), []byte(`{"id":"proj-1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	artifactRoot := filepath.Join(root, "project-state", "proj-1", "artifacts")
	shared := filepath.Join(artifactRoot, "obj-a")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "hello.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := ArtifactManifest{Version: artifactManifestVersion, ProjectID: "proj-1",
		GeneratedAt: time.Now().UTC(), Entries: []ArtifactEntry{{
			ProjectID: "proj-1", ObjectiveID: "obj-a", TaskID: "T1", ArtifactPath: "hello.txt",
			ContentSHA256:      "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
			VerificationStatus: "verified",
		}}}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(artifactRoot, "MANIFEST.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := seedProjectArtifacts(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "repo", "project-artifacts", "obj-a", "hello.txt"))
	if err != nil || string(raw) != "hello world" {
		t.Fatalf("seeded = %q %v", raw, err)
	}
}
