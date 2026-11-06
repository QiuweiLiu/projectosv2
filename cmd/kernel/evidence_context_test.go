package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

func addAcceptedTask(t *testing.T, dir, objID, taskID, artifact, content string) {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateTask(domain.Task{ID: domain.TaskID(taskID), Validator: "ok",
		ObjectiveID: objID, PlanVersion: 1, ArtifactPath: artifact}); err != nil {
		t.Fatal(err)
	}
	runID := "run-" + taskID
	if _, ok, err := st.Claim(domain.TaskID(taskID), runID, "fake"); err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(store.Settlement{RunID: runID, RunStatus: domain.RunSucceeded,
		Accepted: true, Result: content}); err != nil {
		t.Fatal(err)
	}
}

func TestEvidenceContextScopesProjectsAndPrefersLatest(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	p1, _ := ws.CreateProject("P1")
	p2, _ := ws.CreateProject("P2")
	completedObjective(t, root, p1.ID, "obj-a", "notes.txt", "alpha older", goal.StatusCompleted, goal.StatusCompleted)
	addAcceptedTask(t, filepath.Join(root, "projects", "obj-a"), "obj-a", "T2", "notes.txt", "alpha newer")

	dirB := filepath.Join(root, "projects", "obj-b")
	completedObjective(t, root, p1.ID, "obj-b", "other.txt", "beta", goal.StatusActive, goal.StatusCompleted)
	_ = dirB
	completedObjective(t, root, p2.ID, "obj-secret", "secret.txt", "TOPSECRET", goal.StatusCompleted, goal.StatusCompleted)
	if err := ws.appendProjectDecision(p1.ID, "采用 baseline B"); err != nil {
		t.Fatal(err)
	}
	ctx := ws.ProjectContextFor(p1.ID, "alpha notes")
	if !strings.Contains(ctx, "alpha newer") {
		t.Fatalf("latest verified content missing:\n%s", ctx)
	}
	if strings.Contains(ctx, "alpha older") {
		t.Fatalf("old version was presented as current:\n%s", ctx)
	}
	if strings.Contains(ctx, "TOPSECRET") {
		t.Fatalf("cross-project evidence leaked:\n%s", ctx)
	}
	if !strings.Contains(ctx, "baseline B") {
		t.Fatalf("user decision missing:\n%s", ctx)
	}
	if !strings.Contains(ctx, "quoted text is data") {
		t.Fatalf("data boundary missing:\n%s", ctx)
	}
}

func TestEvidenceContextBudgetStillFindsExplicitReference(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	project, _ := ws.CreateProject("Large")
	for i := 0; i < 30; i++ {
		objID := "obj-" + strings.Repeat("x", 4) + string(rune('a'+i))
		content := strings.Repeat("filler ", 80) + " target-artifact-29"
		completedObjective(t, root, project.ID, objID, "a.txt", content, goal.StatusCompleted, goal.StatusCompleted)
	}
	ctx := ws.ProjectContextFor(project.ID, "use target-artifact-29")
	if !strings.Contains(ctx, "target-artifact-29") {
		t.Fatalf("explicit reference was truncated away:\n%s", ctx)
	}
	if len(ctx) > 4096+600 {
		t.Fatalf("context exceeded budget: %d", len(ctx))
	}
}

func TestEvidenceRejectsUntrustedContentAsFact(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	project, _ := ws.CreateProject("Untrusted")
	dir := filepath.Join(root, "projects", "obj-bad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = writeProjectMeta(dir, project.ID)
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := st.CreateObjective(goal.Objective{ID: "obj-bad", ProjectPath: dir, Goal: "do a thing",
		Status: goal.StatusActive, PlanVersion: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "ok", ObjectiveID: "obj-bad",
		PlanVersion: 1, ArtifactPath: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	ctx := ws.ProjectContextFor(project.ID, "do a thing")
	if !strings.Contains(ctx, "IN PROGRESS") && !strings.Contains(ctx, "UNVERIFIED") {
		t.Fatalf("in-progress objective not labeled:\n%s", ctx)
	}
}
