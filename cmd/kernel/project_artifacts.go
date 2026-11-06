package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

// ArtifactManifest is the trusted index for the project shared-artifact tree.
// It is derived from objective databases and delivery.json, never from a scan of
// out/. Deleting it is safe: the next publish rebuilds it from the same facts.
type ArtifactManifest struct {
	Version     int             `json:"version"`
	ProjectID   string          `json:"projectId"`
	GeneratedAt time.Time       `json:"generatedAt"`
	Entries     []ArtifactEntry `json:"entries"`
	Skipped     []ManifestSkip  `json:"skipped,omitempty"`
}

type ArtifactEntry struct {
	ProjectID          string    `json:"projectId"`
	ObjectiveID        string    `json:"objectiveId"`
	TaskID             string    `json:"taskId"`
	ArtifactPath       string    `json:"artifactPath"`
	ContentSHA256      string    `json:"contentSha256"`
	ArtifactVersion    string    `json:"artifactVersion"`
	PlanVersion        int       `json:"planVersion"`
	DeliveryHash       string    `json:"deliveryHash"`
	EvidenceID         string    `json:"evidenceId,omitempty"`
	VerificationStatus string    `json:"verificationStatus"`
	PublishedAt        time.Time `json:"publishedAt"`
}

type ManifestSkip struct {
	ObjectiveID string `json:"objectiveId"`
	Reason      string `json:"reason"`
}

const (
	artifactManifestVersion = 1
	maxSharedFileBytes      = 5 << 20
	maxSharedTotalBytes     = 20 << 20
)

func projectStateDir(root, projectID string) string {
	return filepath.Join(root, "project-state", projectID)
}

func sharedArtifactDir(root, projectID string) string {
	return filepath.Join(projectStateDir(root, projectID), "artifacts")
}

func artifactManifestPath(root, projectID string) string {
	return filepath.Join(sharedArtifactDir(root, projectID), "MANIFEST.json")
}

// publishProjectArtifacts rebuilds the shared tree from authoritative facts.
// Only a completed objective with a completed delivery report can publish.
// out/ is never scanned: a stray file there has no evidence and no hash.
func publishProjectArtifacts(root, projectID string, projects []project) (ArtifactManifest, error) {
	manifest := ArtifactManifest{Version: artifactManifestVersion, ProjectID: projectID,
		GeneratedAt: time.Now().UTC()}
	state := projectStateDir(root, projectID)
	if err := os.MkdirAll(state, 0o755); err != nil {
		return manifest, fmt.Errorf("create project state: %w", err)
	}
	unlock, err := lockProjectPublish(state)
	if err != nil {
		return manifest, err
	}
	defer unlock()
	staging, err := os.MkdirTemp(state, ".artifacts-staging-")
	if err != nil {
		return manifest, fmt.Errorf("create artifact staging: %w", err)
	}
	defer os.RemoveAll(staging)

	var total int64
	seenPaths := map[string]string{}
	for _, p := range projects {
		if p.ProjectID != projectID || p.Obj.Status != goal.StatusCompleted {
			continue
		}
		entries, skip, err := publishableArtifacts(p, staging, &total, seenPaths)
		if err != nil {
			return manifest, fmt.Errorf("objective %s: %w", p.Obj.ID, err)
		}
		if skip != "" {
			manifest.Skipped = append(manifest.Skipped, ManifestSkip{ObjectiveID: p.Obj.ID, Reason: skip})
			continue
		}
		manifest.Entries = append(manifest.Entries, entries...)
	}
	sort.Slice(manifest.Entries, func(i, j int) bool {
		if manifest.Entries[i].ObjectiveID == manifest.Entries[j].ObjectiveID {
			return manifest.Entries[i].ArtifactPath < manifest.Entries[j].ArtifactPath
		}
		return manifest.Entries[i].ObjectiveID < manifest.Entries[j].ObjectiveID
	})

	// The manifest lives inside the tree, so one directory swap publishes files
	// and provenance together.
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return manifest, err
	}
	if err := os.WriteFile(filepath.Join(staging, "MANIFEST.json"), append(raw, '\n'), 0o644); err != nil {
		return manifest, fmt.Errorf("write artifact manifest: %w", err)
	}
	if err := swapArtifactTree(state, staging); err != nil {
		return manifest, err
	}
	return manifest, nil
}

// lockProjectPublish serializes publishers across processes. The goal runner and
// the serve process can both refresh the same project; a flock prevents one
// atomic swap from clobbering the other.
func lockProjectPublish(state string) (func(), error) {
	file, err := os.OpenFile(filepath.Join(state, ".publish.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open publish lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, fmt.Errorf("lock publish: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

func publishableArtifacts(p project, staging string, total *int64, seen map[string]string) ([]ArtifactEntry, string, error) {
	now := time.Now().UTC()
	deliveryPath := filepath.Join(p.Dir, "delivery.json")
	raw, err := os.ReadFile(deliveryPath)
	if err != nil {
		return nil, "completed objective has no readable delivery.json", nil
	}
	var report struct {
		Status string `json:"status"`
		Checks []struct {
			OK bool `json:"ok"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, "delivery.json is not valid JSON", nil
	}
	if report.Status != string(goal.StatusCompleted) {
		return nil, "delivery.json status is " + report.Status, nil
	}
	for _, check := range report.Checks {
		if !check.OK {
			return nil, "delivery command check failed", nil
		}
	}
	deliverySum := sha256.Sum256(raw)
	deliveryHash := hex.EncodeToString(deliverySum[:])

	st, err := store.OpenReadOnly(filepath.Join(p.Dir, "kernel.db"))
	if err != nil {
		return nil, "", fmt.Errorf("open objective database: %w", err)
	}
	defer st.Close()
	if blocking, found, err := latestObjectiveReview(st, p.Obj.ID); err != nil {
		return nil, "", err
	} else if found && blocking {
		return nil, "latest objective acceptance review is blocking", nil
	}
	tasks, err := st.List()
	if err != nil {
		return nil, "", err
	}
	var entries []ArtifactEntry
	for _, task := range tasks {
		if task.ObjectiveID != p.Obj.ID {
			continue
		}
		if task.Status != domain.TaskAccepted || task.ArtifactPath == "" {
			continue
		}
		rel, err := cleanArtifactRel(task.ArtifactPath)
		if err != nil {
			return nil, "", fmt.Errorf("task %s: %w", task.ID, err)
		}
		key := p.Obj.ID + "\x00" + rel
		if other, ok := seen[key]; ok {
			return nil, "", fmt.Errorf("tasks %s and %s publish the same artifact %q", other, task.ID, rel)
		}
		content, err := st.AcceptedResult(task.ID)
		if err != nil {
			return nil, "", fmt.Errorf("task %s has no accepted artifact: %w", task.ID, err)
		}
		if len(content) > maxSharedFileBytes {
			return nil, "", fmt.Errorf("artifact %s exceeds %d bytes", rel, maxSharedFileBytes)
		}
		if *total+int64(len(content)) > maxSharedTotalBytes {
			return nil, "", fmt.Errorf("project shared artifacts exceed %d bytes", maxSharedTotalBytes)
		}
		sum := sha256.Sum256([]byte(content))
		digest := hex.EncodeToString(sum[:])
		evidence, err := latestAcceptedEvidence(st, task.ID)
		if err != nil {
			return nil, "", err
		}
		if evidence.Digest == "" {
			return nil, fmt.Sprintf("task %s has no accepted evidence", task.ID), nil
		}
		if evidence.Digest != digest {
			return nil, "", fmt.Errorf("task %s accepted result does not match evidence digest", task.ID)
		}
		target := filepath.Join(staging, p.Obj.ID, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, "", fmt.Errorf("create artifact directory: %w", err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return nil, "", fmt.Errorf("write shared artifact %s: %w", rel, err)
		}
		wrote, err := os.ReadFile(target)
		if err != nil || sha256.Sum256(wrote) != sum {
			return nil, "", fmt.Errorf("shared artifact %s failed content verification", rel)
		}
		*total += int64(len(content))
		seen[key] = string(task.ID)
		entries = append(entries, ArtifactEntry{
			ProjectID: p.ProjectID, ObjectiveID: p.Obj.ID, TaskID: string(task.ID),
			ArtifactPath: filepath.ToSlash(rel), ContentSHA256: digest,
			ArtifactVersion: evidence.ArtifactVersion, PlanVersion: task.PlanVersion,
			DeliveryHash: deliveryHash, EvidenceID: evidence.ID,
			VerificationStatus: "verified", PublishedAt: now,
		})
	}
	if len(entries) == 0 {
		return nil, "completed objective has no accepted artifacts", nil
	}
	return entries, "", nil
}

func latestObjectiveReview(st *store.Store, objectiveID string) (bool, bool, error) {
	reviews, err := st.ReviewsFor(domain.TaskID("objective:" + objectiveID))
	if err != nil || len(reviews) == 0 {
		return false, false, err
	}
	last := reviews[len(reviews)-1]
	return last.Blocking(), true, nil
}

func latestAcceptedEvidence(st *store.Store, taskID domain.TaskID) (domain.Evidence, error) {
	all, err := st.EvidenceFor(taskID)
	if err != nil {
		return domain.Evidence{}, err
	}
	var latest domain.Evidence
	for _, ev := range all {
		if ev.Accepted {
			latest = ev
		}
	}
	return latest, nil
}

func cleanPathSegment(segment string) error {
	if segment == "" || segment == "." || segment == ".." || strings.ContainsAny(segment, `/\`) {
		return fmt.Errorf("invalid path segment %q", segment)
	}
	return nil
}

func cleanArtifactRel(path string) (string, error) {
	rel := filepath.Clean(strings.TrimSpace(path))
	if rel == "" || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact path escapes the shared tree: %q", path)
	}
	return rel, nil
}

// swapArtifactTree replaces the shared tree with the staging directory. The old
// tree is kept until the new one is in place, then removed best-effort.
func swapArtifactTree(state, staging string) error {
	target := filepath.Join(state, "artifacts")
	old := filepath.Join(state, fmt.Sprintf(".artifacts-old-%d", time.Now().UnixNano()))
	hadOld := false
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, old); err != nil {
			return fmt.Errorf("replace shared artifacts: %w", err)
		}
		hadOld = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect shared artifacts: %w", err)
	}
	if err := os.Rename(staging, target); err != nil {
		if hadOld {
			_ = os.Rename(old, target)
		}
		return fmt.Errorf("install shared artifacts: %w", err)
	}
	if hadOld {
		_ = os.RemoveAll(old)
	}
	return nil
}

func readProjectManifest(root, projectID string) (ArtifactManifest, error) {
	raw, err := os.ReadFile(artifactManifestPath(root, projectID))
	if err != nil {
		return ArtifactManifest{}, err
	}
	var manifest ArtifactManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return ArtifactManifest{}, fmt.Errorf("artifact manifest: %w", err)
	}
	if manifest.Version != artifactManifestVersion {
		return ArtifactManifest{}, fmt.Errorf("unsupported artifact manifest version %d", manifest.Version)
	}
	if manifest.ProjectID != projectID {
		return ArtifactManifest{}, fmt.Errorf("artifact manifest belongs to project %s", manifest.ProjectID)
	}
	return manifest, nil
}

// seedProjectArtifacts exposes only manifest-listed, hash-verified artifacts in
// a fresh namespace inside the worker's seed directory. A file that happens to
// exist in the shared tree but is absent from the manifest is ignored.
func seedProjectArtifacts(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "project.json"))
	if err != nil {
		return nil
	}
	var meta struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &meta) != nil || strings.TrimSpace(meta.ID) == "" {
		return nil
	}
	root := dir
	parent := filepath.Dir(dir)
	if filepath.Base(parent) == "projects" {
		root = filepath.Dir(parent)
	}
	manifest, err := readProjectManifest(root, meta.ID)
	if err != nil {
		if os.IsNotExist(err) {
			// No manifest means no trusted evidence. Clear any legacy copy rather
			// than letting stale files masquerade as published artifacts.
			_ = os.RemoveAll(filepath.Join(dir, "repo", "project-artifacts"))
			return nil
		}
		return fmt.Errorf("read shared artifact manifest: %w", err)
	}
	source := sharedArtifactDir(root, meta.ID)
	dest := filepath.Join(dir, "repo", "project-artifacts")
	// Verify everything before clearing the old seed. A tampered entry must not
	// leave the worker with a half-updated namespace.
	type seedFile struct {
		rel string
		raw []byte
	}
	var files []seedFile
	for _, entry := range manifest.Entries {
		if entry.ProjectID != meta.ID {
			return fmt.Errorf("manifest entry for project %s cannot seed project %s", entry.ProjectID, meta.ID)
		}
		if err := cleanPathSegment(entry.ObjectiveID); err != nil {
			return fmt.Errorf("manifest objective: %w", err)
		}
		rel, err := cleanArtifactRel(entry.ArtifactPath)
		if err != nil {
			return err
		}
		from := filepath.Join(source, entry.ObjectiveID, rel)
		raw, err := os.ReadFile(from)
		if err != nil {
			return fmt.Errorf("shared artifact %s/%s: %w", entry.ObjectiveID, rel, err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != entry.ContentSHA256 {
			return fmt.Errorf("shared artifact %s/%s does not match its manifest hash", entry.ObjectiveID, rel)
		}
		files = append(files, seedFile{rel: filepath.Join(entry.ObjectiveID, rel), raw: raw})
	}
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("clear seed artifact copy: %w", err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, file := range files {
		to := filepath.Join(dest, file.rel)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(to, file.raw, 0o644); err != nil {
			return err
		}
	}
	// The worker may edit its copy; this manifest is provenance, not authority.
	copyManifest := manifest
	copyManifest.Entries = append([]ArtifactEntry(nil), manifest.Entries...)
	encoded, err := json.MarshalIndent(copyManifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dest, "MANIFEST.json"), append(encoded, '\n'), 0o644)
}

// publishProjectArtifactsForDir is the completion hook: it finds the owning
// project and refreshes its derived tree without changing objective status.
func readManifestForObjective(dir string) (ArtifactManifest, error) {
	projectID := readProjectMeta(dir)
	root := dir
	parent := filepath.Dir(dir)
	if filepath.Base(parent) == "projects" {
		root = filepath.Dir(parent)
	}
	return readProjectManifest(root, projectID)
}

func publishProjectArtifactsForDir(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "project.json")); err != nil {
		return nil // a standalone CLI state dir has no project namespace
	}
	projectID := readProjectMeta(dir)
	if projectID == "" {
		return nil
	}
	root := dir
	parent := filepath.Dir(dir)
	if filepath.Base(parent) == "projects" {
		root = filepath.Dir(parent)
	}
	ws := newWorkspace(root)
	all, err := ws.list()
	if err != nil {
		return err
	}
	_, err = publishProjectArtifacts(root, projectID, all)
	return err
}
