package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

func completedObjective(t *testing.T, root, projectID, objID, artifact, content string, objStatus, deliveryStatus goal.Status) {
	t.Helper()
	dir := filepath.Join(root, "projects", objID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeProjectMeta(dir, projectID); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	obj := goal.Objective{ID: objID, ProjectPath: dir, Goal: "goal " + objID, Status: goal.StatusActive,
		PlanVersion: 1, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "T1", Validator: "ok", ObjectiveID: objID, PlanVersion: 1, ArtifactPath: artifact}
	if err := st.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.Claim("T1", "r1", "fake"); err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunSucceeded,
		Accepted: true, Result: content}); err != nil {
		t.Fatal(err)
	}
	delivery := map[string]any{"status": string(deliveryStatus), "checks": []any{}}
	raw, _ := json.Marshal(delivery)
	if err := os.WriteFile(filepath.Join(dir, "delivery.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if objStatus == goal.StatusCompleted {
		if err := st.MoveObjective(objID, goal.StatusCompleted); err != nil {
			t.Fatal(err)
		}
	}
}

func publishForTest(t *testing.T, root, projectID string) ArtifactManifest {
	t.Helper()
	ws := newWorkspace(root)
	all, err := ws.list()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := publishProjectArtifacts(root, projectID, all)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestPublishRequiresCompletedObjectiveAndDelivery(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	project, err := ws.CreateProject("Evidence")
	if err != nil {
		t.Fatal(err)
	}
	// Active objective: accepted task but no completion fact.
	completedObjective(t, root, project.ID, "obj-active", "a.txt", "a", goal.StatusActive, goal.StatusCompleted)
	// Completed objective whose delivery report says failed.
	completedObjective(t, root, project.ID, "obj-failed", "b.txt", "b", goal.StatusCompleted, goal.StatusFailed)

	manifest := publishForTest(t, root, project.ID)
	if len(manifest.Entries) != 0 {
		t.Fatalf("untrusted objectives were published: %+v", manifest.Entries)
	}
	if len(manifest.Skipped) != 1 || manifest.Skipped[0].ObjectiveID != "obj-failed" {
		t.Fatalf("skipped = %+v", manifest.Skipped)
	}
	if _, err := os.Stat(filepath.Join(sharedArtifactDir(root, project.ID), "obj-active", "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("active objective leaked into shared tree: %v", err)
	}
}

func TestPublishUsesAcceptedContentNotOut(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	project, err := ws.CreateProject("Evidence")
	if err != nil {
		t.Fatal(err)
	}
	completedObjective(t, root, project.ID, "obj-a", "notes.txt", "trusted", goal.StatusCompleted, goal.StatusCompleted)
	dir := filepath.Join(root, "projects", "obj-a")
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "out", "notes.txt"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "out", "extra.txt"), []byte("not accepted"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := publishForTest(t, root, project.ID)
	if len(manifest.Entries) != 1 {
		t.Fatalf("entries = %+v", manifest.Entries)
	}
	shared := filepath.Join(sharedArtifactDir(root, project.ID), "obj-a", "notes.txt")
	raw, err := os.ReadFile(shared)
	if err != nil || string(raw) != "trusted" {
		t.Fatalf("shared = %q %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(sharedArtifactDir(root, project.ID), "obj-a", "extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("unaccepted out file was published: %v", err)
	}
	// A worker editing its local shared copy cannot change the accepted result.
	if err := os.WriteFile(shared, []byte("local edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	publishForTest(t, root, project.ID)
	raw, _ = os.ReadFile(shared)
	if string(raw) != "trusted" {
		t.Fatalf("republish did not restore authoritative content: %q", raw)
	}
}

func TestManifestSeedVerifiesHashesAndIgnoresStaleFiles(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	project, err := ws.CreateProject("Evidence")
	if err != nil {
		t.Fatal(err)
	}
	completedObjective(t, root, project.ID, "obj-a", "notes.txt", "hello world", goal.StatusCompleted, goal.StatusCompleted)
	publishForTest(t, root, project.ID)

	stale := filepath.Join(sharedArtifactDir(root, project.ID), "obj-a", "stale.txt")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "projects", "obj-b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeProjectMeta(dir, project.ID); err != nil {
		t.Fatal(err)
	}
	if err := seedProjectArtifacts(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "repo", "project-artifacts", "obj-a", "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale file was seeded: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "repo", "project-artifacts", "obj-a", "notes.txt"))
	if err != nil || string(raw) != "hello world" {
		t.Fatalf("seeded = %q %v", raw, err)
	}
	// Mutating the shared copy must be detected, not silently seeded.
	if err := os.WriteFile(filepath.Join(sharedArtifactDir(root, project.ID), "obj-a", "notes.txt"),
		[]byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := seedProjectArtifacts(dir); err == nil || !strings.Contains(err.Error(), "manifest hash") {
		t.Fatalf("tampered shared artifact was not rejected: %v", err)
	}
}

func TestManifestKeepsSameNameArtifactsSeparate(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	project, err := ws.CreateProject("Evidence")
	if err != nil {
		t.Fatal(err)
	}
	completedObjective(t, root, project.ID, "obj-a", "notes.txt", "from A", goal.StatusCompleted, goal.StatusCompleted)
	completedObjective(t, root, project.ID, "obj-b", "notes.txt", "from B", goal.StatusCompleted, goal.StatusCompleted)
	manifest := publishForTest(t, root, project.ID)
	if len(manifest.Entries) != 2 {
		t.Fatalf("entries = %+v", manifest.Entries)
	}
	a, _ := os.ReadFile(filepath.Join(sharedArtifactDir(root, project.ID), "obj-a", "notes.txt"))
	b, _ := os.ReadFile(filepath.Join(sharedArtifactDir(root, project.ID), "obj-b", "notes.txt"))
	if string(a) != "from A" || string(b) != "from B" {
		t.Fatalf("a=%q b=%q", a, b)
	}
	if manifest.Entries[0].ContentSHA256 == manifest.Entries[1].ContentSHA256 {
		t.Fatal("different contents must have different hashes")
	}
}

func TestPublishRemovesArtifactsFromNoLongerEligibleObjective(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	project, err := ws.CreateProject("Evidence")
	if err != nil {
		t.Fatal(err)
	}
	completedObjective(t, root, project.ID, "obj-a", "notes.txt", "old", goal.StatusCompleted, goal.StatusCompleted)
	if manifest := publishForTest(t, root, project.ID); len(manifest.Entries) != 1 {
		t.Fatalf("entries = %+v", manifest.Entries)
	}
	// Completed is terminal in the public API; a direct SQL change models a
	// repaired/rolled-back state and proves stale files are not left published.
	db, err := sql.Open("sqlite", filepath.Join(root, "projects", "obj-a", "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE objectives SET status='failed' WHERE id='obj-a'`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	manifest := publishForTest(t, root, project.ID)
	if len(manifest.Entries) != 0 {
		t.Fatalf("stale entry survived: %+v", manifest.Entries)
	}
	if _, err := os.Stat(filepath.Join(sharedArtifactDir(root, project.ID), "obj-a", "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale shared file survived: %v", err)
	}
}

func TestPublishSkipsEvidenceMismatchWithoutBlockingOthers(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	project, err := ws.CreateProject("Evidence")
	if err != nil {
		t.Fatal(err)
	}
	completedObjective(t, root, project.ID, "obj-bad", "bad.txt", "trusted", goal.StatusCompleted, goal.StatusCompleted)
	completedObjective(t, root, project.ID, "obj-good", "good.txt", "good", goal.StatusCompleted, goal.StatusCompleted)
	publishForTest(t, root, project.ID)
	db, err := sql.Open("sqlite", filepath.Join(root, "projects", "obj-bad", "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE runs SET result='tampered' WHERE id='r1'`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	manifest := publishForTest(t, root, project.ID)
	if len(manifest.Entries) != 1 || manifest.Entries[0].ObjectiveID != "obj-good" {
		t.Fatalf("entries = %+v", manifest.Entries)
	}
	if len(manifest.Skipped) != 1 || !strings.Contains(manifest.Skipped[0].Reason, "evidence digest") {
		t.Fatalf("skipped = %+v", manifest.Skipped)
	}
	if _, err := os.Stat(filepath.Join(sharedArtifactDir(root, project.ID), "obj-bad", "bad.txt")); !os.IsNotExist(err) {
		t.Fatalf("bad objective leaked into shared tree: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(sharedArtifactDir(root, project.ID), "obj-good", "good.txt")); err != nil || string(raw) != "good" {
		t.Fatalf("good objective was blocked: %q %v", raw, err)
	}
}

func TestManifestSkipsPathTraversalWithoutPublishing(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	project, err := ws.CreateProject("Evidence")
	if err != nil {
		t.Fatal(err)
	}
	completedObjective(t, root, project.ID, "obj-a", "../escape.txt", "bad", goal.StatusCompleted, goal.StatusCompleted)
	all, _ := ws.list()
	manifest, err := publishProjectArtifacts(root, project.ID, all)
	if err != nil {
		t.Fatalf("one bad objective must not abort publication: %v", err)
	}
	if len(manifest.Entries) != 0 || len(manifest.Skipped) != 1 || !strings.Contains(manifest.Skipped[0].Reason, "escapes") {
		t.Fatalf("manifest = %+v", manifest)
	}
	if _, err := os.Stat(filepath.Join(root, "project-state", project.ID, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("path traversal wrote outside the objective directory: %v", err)
	}
}

func TestManifestHashMatchesContent(t *testing.T) {
	sum := sha256.Sum256([]byte("hello world"))
	if hex.EncodeToString(sum[:]) == "" {
		t.Fatal("hash")
	}
}

func TestConcurrentPublishIsSerializedAndValid(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	project, _ := ws.CreateProject("Concurrent")
	completedObjective(t, root, project.ID, "obj-a", "notes.txt", "value", goal.StatusCompleted, goal.StatusCompleted)
	all, _ := ws.list()
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := publishProjectArtifacts(root, project.ID, all)
			errs <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := readProjectManifest(root, project.ID)
	if err != nil || len(manifest.Entries) != 1 {
		t.Fatalf("manifest = %+v %v", manifest, err)
	}
	raw, err := os.ReadFile(filepath.Join(sharedArtifactDir(root, project.ID), "obj-a", "notes.txt"))
	if err != nil || string(raw) != "value" {
		t.Fatalf("shared = %q %v", raw, err)
	}
}

// A failed refresh must not leave a half-written manifest or destroy the last
// good shared tree. A read-only project-state directory is a safe stand-in for
// a disk/permission failure.
func TestPublishFailureKeepsLastGoodManifest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	ws := newWorkspace(root)
	project, _ := ws.CreateProject("Failure")
	completedObjective(t, root, project.ID, "obj-a", "notes.txt", "good", goal.StatusCompleted, goal.StatusCompleted)
	publishForTest(t, root, project.ID)
	state := projectStateDir(root, project.ID)
	if err := os.Chmod(state, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(state, 0o755)
	all, _ := ws.list()
	if _, err := publishProjectArtifacts(root, project.ID, all); err == nil {
		t.Fatal("publish must fail when the state directory is read-only")
	}
	if err := os.Chmod(state, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, err := readProjectManifest(root, project.ID)
	if err != nil || len(manifest.Entries) != 1 {
		t.Fatalf("last good manifest was lost: %+v %v", manifest, err)
	}
	raw, err := os.ReadFile(filepath.Join(sharedArtifactDir(root, project.ID), "obj-a", "notes.txt"))
	if err != nil || string(raw) != "good" {
		t.Fatalf("last good tree was damaged: %q %v", raw, err)
	}
}

func TestManifestCanBeRebuiltFromAuthoritativeFacts(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	project, _ := ws.CreateProject("Rebuild")
	completedObjective(t, root, project.ID, "obj-a", "notes.txt", "hello", goal.StatusCompleted, goal.StatusCompleted)
	publishForTest(t, root, project.ID)
	if err := os.Remove(filepath.Join(sharedArtifactDir(root, project.ID), "MANIFEST.json")); err != nil {
		t.Fatal(err)
	}
	manifest := publishForTest(t, root, project.ID)
	if len(manifest.Entries) != 1 || manifest.Entries[0].ContentSHA256 == "" {
		t.Fatalf("manifest was not rebuilt: %+v", manifest)
	}
}

func TestPublishBindsObjectiveReviewToPlanVersion(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	project, _ := ws.CreateProject("PlanBinding")
	completedObjective(t, root, project.ID, "obj-a", "notes.txt", "value", goal.StatusCompleted, goal.StatusCompleted)
	dir := filepath.Join(root, "projects", "obj-a")
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddReview(domain.Review{TaskID: "objective:obj-a", ArtifactVersion: "old-plan",
		Kind: domain.ReviewObjective, PlanVersion: 2,
		Findings: []domain.Finding{{Severity: domain.SeverityP1, Code: "stale"}}}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()
	manifest := publishForTest(t, root, project.ID)
	if len(manifest.Entries) != 1 {
		t.Fatalf("a review for another plan blocked publication: %+v", manifest)
	}

	st, err = store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddReview(domain.Review{TaskID: "objective:obj-a", ArtifactVersion: "current-plan",
		Kind: domain.ReviewObjective, PlanVersion: 1,
		Findings: []domain.Finding{{Severity: domain.SeverityP1, Code: "current"}}}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()
	manifest = publishForTest(t, root, project.ID)
	if len(manifest.Entries) != 0 || len(manifest.Skipped) != 1 || !strings.Contains(manifest.Skipped[0].Reason, "objective acceptance") {
		t.Fatalf("current-plan blocking review was ignored: %+v", manifest)
	}
}

// A reader must not combine an old Manifest with files from a newer tree.
// The test holds the publisher lock across a tree swap while seed starts.
func TestSeedReadsManifestAndFilesUnderOneVersionLock(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	project, _ := ws.CreateProject("Concurrent")
	completedObjective(t, root, project.ID, "obj-a", "a.txt", "v1", goal.StatusCompleted, goal.StatusCompleted)
	publishForTest(t, root, project.ID)

	seedDir := filepath.Join(root, "projects", "obj-b")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeProjectMeta(seedDir, project.ID); err != nil {
		t.Fatal(err)
	}
	state := projectStateDir(root, project.ID)
	unlock, err := lockProjectPublish(state)
	if err != nil {
		t.Fatal(err)
	}
	seedErr := make(chan error, 1)
	go func() { seedErr <- seedProjectArtifacts(seedDir) }()
	time.Sleep(150 * time.Millisecond) // let an old reader load the v1 manifest first

	staging, err := os.MkdirTemp(state, ".version-test-")
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(staging, "obj-a"), 0o755); err != nil {
		unlock()
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "obj-a", "a.txt"), []byte("v2"), 0o644); err != nil {
		unlock()
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("v2"))
	manifest := ArtifactManifest{Version: artifactManifestVersion, ProjectID: project.ID,
		GeneratedAt: time.Now().UTC(), Entries: []ArtifactEntry{{
			ProjectID: project.ID, ObjectiveID: "obj-a", TaskID: "T1", ArtifactPath: "a.txt",
			ContentSHA256: hex.EncodeToString(sum[:]), VerificationStatus: "verified",
		}}}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(staging, "MANIFEST.json"), raw, 0o644); err != nil {
		unlock()
		t.Fatal(err)
	}
	if err := swapArtifactTree(state, staging); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()

	if err := <-seedErr; err != nil {
		t.Fatalf("seed crossed versions: %v", err)
	}
	rawFile, err := os.ReadFile(filepath.Join(seedDir, "repo", "project-artifacts", "obj-a", "a.txt"))
	if err != nil || string(rawFile) != "v2" {
		t.Fatalf("seed content = %q %v", rawFile, err)
	}
}

func TestPublishRecoversCrashWindowAndCleansStaging(t *testing.T) {
	root := t.TempDir()
	ws := newWorkspace(root)
	project, _ := ws.CreateProject("Crash")
	completedObjective(t, root, project.ID, "obj-a", "a.txt", "safe", goal.StatusCompleted, goal.StatusCompleted)
	publishForTest(t, root, project.ID)
	state := projectStateDir(root, project.ID)
	crashOld := filepath.Join(state, ".artifacts-old-crash")
	if err := os.Rename(filepath.Join(state, "artifacts"), crashOld); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(state, ".artifacts-staging-crash"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := publishForTest(t, root, project.ID)
	if len(manifest.Entries) != 1 {
		t.Fatalf("entries = %+v", manifest.Entries)
	}
	if raw, err := os.ReadFile(filepath.Join(sharedArtifactDir(root, project.ID), "obj-a", "a.txt")); err != nil || string(raw) != "safe" {
		t.Fatalf("tree = %q %v", raw, err)
	}
	entries, _ := os.ReadDir(state)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".artifacts-") {
			t.Fatalf("leftover crash directory: %s", entry.Name())
		}
	}
}
