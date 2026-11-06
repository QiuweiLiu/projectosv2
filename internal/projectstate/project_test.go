package projectstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectSnapshotIsBoundedAndReadable(t *testing.T) {
	root := t.TempDir()
	var objectives []ProjectObjective
	for i := 0; i < 25; i++ {
		objectives = append(objectives, ProjectObjective{
			ID: "obj-" + string(rune('a'+i)), Goal: strings.Repeat("目标", 200),
			Status: "active", PlanVersion: 1, Accepted: i, Total: i + 1,
			Blockers:  []string{strings.Repeat("阻塞", 100)},
			UpdatedAt: time.Now().Add(time.Duration(i) * time.Minute),
		})
	}
	snap := BuildProject(ProjectMeta{ID: "proj-1", Name: "GPU Scheduling", Description: "long context"}, objectives, []string{"decision one", "decision two"})
	for i := 0; i < 50; i++ {
		snap.AddVerified("obj-a", "T1", "a.txt")
	}
	if err := WriteProject(root, snap); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"PROJECT.md", "STATE.md", "HANDOFF.md", "DECISIONS.md", SnapshotFile} {
		info, err := os.Stat(filepath.Join(ProjectDir(root, "proj-1"), name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Size() > ProjectMaxFileBytes {
			t.Fatalf("%s = %d bytes", name, info.Size())
		}
	}
	got, ok, err := ReadProject(root, "proj-1")
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if got.Hash == "" || len(got.Objectives) > projectMaxObjectives || len(got.Verified) > projectMaxVerified {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestProjectSnapshotRendersBlockersAndVerified(t *testing.T) {
	snap := BuildProject(ProjectMeta{ID: "p", Name: "P"}, []ProjectObjective{{
		ID: "obj-1", Goal: "ship", Status: "failed", PlanVersion: 2, Accepted: 1, Total: 2,
		Blockers: []string{"T2 blocked: missing input"},
	}}, nil)
	snap.AddVerified("obj-1", "T1", "a.txt")
	files := renderProject(snap)
	if !strings.Contains(files["STATE.md"], "T2 blocked") && !strings.Contains(files["STATE.md"], "missing input") {
		t.Fatalf("state = %s", files["STATE.md"])
	}
	if !strings.Contains(files["PROJECT.md"], "obj-1/T1 a.txt") {
		t.Fatalf("project = %s", files["PROJECT.md"])
	}
}
