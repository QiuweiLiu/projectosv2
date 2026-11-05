package projectstate_test

import (
	"os"
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/projectstate"
	"projectos/kernel/internal/store"
)

func setup(t *testing.T) (*store.Store, goal.Objective, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	obj := goal.Objective{ID: "obj-1", Title: "demo", Goal: "produce alpha", Status: goal.StatusActive,
		Acceptance: "a.txt equals alpha", PlanVersion: 1}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "T1", ArtifactPath: "a.txt",
		ObjectiveID: obj.ID, PlanVersion: 1}); err != nil {
		t.Fatal(err)
	}
	return st, obj, dir
}

func TestWriteReadAndDrift(t *testing.T) {
	st, obj, dir := setup(t)
	snap, err := projectstate.Build(st, obj, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := projectstate.Write(dir, snap); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"PROJECT.md", "STATE.md", "HANDOFF.md", "snapshot.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, ".project", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(raw) > projectstate.MaxFileBytes && name != "snapshot.json" {
			t.Fatalf("%s exceeds budget: %d", name, len(raw))
		}
	}
	stored, ok, err := projectstate.Read(dir)
	if err != nil || !ok {
		t.Fatalf("read: %v %v", ok, err)
	}
	if stored.Hash != snap.Hash {
		t.Fatal("stored hash differs")
	}
	drift, err := projectstate.Drift(st, obj, dir)
	if err != nil || drift {
		t.Fatalf("fresh snapshot should not drift: %v %v", drift, err)
	}

	// Change runtime state without exporting: the snapshot must be reported stale.
	if _, ok, _ := st.Claim("T1", "run-1", "fake"); !ok {
		t.Fatal("claim failed")
	}
	drift, err = projectstate.Drift(st, obj, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !drift {
		t.Fatal("database change must be detected as drift")
	}
}

func TestMissingSnapshotIsNotAnError(t *testing.T) {
	_, _, dir := setup(t)
	_, ok, err := projectstate.Read(dir)
	if err != nil || ok {
		t.Fatalf("missing snapshot: ok=%v err=%v", ok, err)
	}
}

func TestRenderStaysBoundedWithManyTasks(t *testing.T) {
	st, obj, dir := setup(t)
	for i := 0; i < 19; i++ {
		id := string(rune('A'+i)) + string(rune('A'+i))
		if err := st.CreateTask(domain.Task{ID: domain.TaskID(id), Validator: id, ArtifactPath: "x.txt",
			ObjectiveID: obj.ID, PlanVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := projectstate.Build(st, obj, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := projectstate.Write(dir, snap); err != nil {
		t.Fatalf("20 tasks must still fit the budget: %v", err)
	}
}
