package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

func TestServeDiffComparesWorkAgainstDelivery(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateObjective(goal.Objective{ID: "obj-1", Goal: "g", Status: goal.StatusActive}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(domain.Task{ID: "T1", ObjectiveID: "obj-1", PlanVersion: 1,
		ArtifactPath: "a.txt", Validator: "nonempty"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "out", "a.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work", "T1-1-1")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/diff?objective=obj-1&task=T1", nil)
	serveDiff(rec, req, newWorkspace(dir))
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var view diffView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.First || !view.Changed || view.Diff != "-old\n+new\n" {
		t.Fatalf("view = %+v", view)
	}
}

func TestServeDiffRejectsEscapingSymlink(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateObjective(goal.Objective{ID: "obj-1", Goal: "g", Status: goal.StatusActive}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(domain.Task{ID: "T1", ObjectiveID: "obj-1", PlanVersion: 1,
		ArtifactPath: "a.txt", Validator: "nonempty"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "out", "a.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work", "T1-1-1")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(work, "a.txt")); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/diff?objective=obj-1&task=T1", nil)
	serveDiff(rec, req, newWorkspace(dir))
	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
