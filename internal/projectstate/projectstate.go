// Package projectstate renders a bounded, human-readable snapshot of one
// objective. SQLite stays authoritative; the files are a derived view.
package projectstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

const (
	MaxFileBytes   = 4096
	SnapshotFile   = "snapshot.json"
	GeneratedByTag = "generated-by: projectos-kernel"
)

type TaskSnap struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Attempts int    `json:"attempts"`
	Artifact string `json:"artifact,omitempty"`
	Error    string `json:"error,omitempty"`
}

type Snapshot struct {
	ObjectiveID string     `json:"objectiveId"`
	Title       string     `json:"title"`
	Goal        string     `json:"goal"`
	Status      string     `json:"status"`
	PlanVersion int        `json:"planVersion"`
	Acceptance  string     `json:"acceptance,omitempty"`
	Tasks       []TaskSnap `json:"tasks"`
	HasDelivery bool       `json:"hasDelivery"`
	GeneratedAt time.Time  `json:"generatedAt"`
	Hash        string     `json:"hash"`
}

func Build(st *store.Store, obj goal.Objective, dir string) (Snapshot, error) {
	tasks, err := st.List()
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{ObjectiveID: obj.ID, Title: obj.Title, Goal: obj.Goal, Status: string(obj.Status),
		PlanVersion: obj.PlanVersion, Acceptance: obj.Acceptance, GeneratedAt: time.Now().UTC()}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	for _, t := range tasks {
		snap.Tasks = append(snap.Tasks, TaskSnap{ID: string(t.ID), Status: string(t.Status),
			Attempts: t.Attempts, Artifact: t.ArtifactPath, Error: t.LastError})
	}
	if _, err := os.Stat(filepath.Join(dir, "delivery.md")); err == nil {
		snap.HasDelivery = true
	}
	snap.Hash = hash(render(snap))
	return snap, nil
}

func render(s Snapshot) map[string]string {
	var state strings.Builder
	state.WriteString("# State\n\n")
	fmt.Fprintf(&state, "Objective: %s (%s)\n", s.Title, s.Status)
	fmt.Fprintf(&state, "Plan version: v%d\n\n", s.PlanVersion)
	state.WriteString("| Task | Status | Attempts | Artifact | Note |\n|---|---|---|---|---|\n")
	for _, t := range s.Tasks {
		note := t.Error
		if note == "" {
			note = "-"
		}
		fmt.Fprintf(&state, "| %s | %s | %d | %s | %s |\n", t.ID, t.Status, t.Attempts, t.Artifact, note)
	}
	state.WriteString("\n" + GeneratedByTag + "\n")

	var project strings.Builder
	project.WriteString("# Project\n\n")
	fmt.Fprintf(&project, "## Goal\n\n%s\n\n", s.Goal)
	if s.Acceptance != "" {
		fmt.Fprintf(&project, "## Acceptance\n\n%s\n\n", s.Acceptance)
	}
	project.WriteString(GeneratedByTag + "\n")

	var handoff strings.Builder
	handoff.WriteString("# Handoff\n\n")
	fmt.Fprintf(&handoff, "Status: %s\n", s.Status)
	fmt.Fprintf(&handoff, "Plan: v%d\n", s.PlanVersion)
	if s.HasDelivery {
		handoff.WriteString("Delivery: delivery.md\n")
	}
	blocked := 0
	for _, t := range s.Tasks {
		if t.Status != string(domain.TaskAccepted) && t.Status != string(domain.TaskSuperseded) {
			blocked++
		}
	}
	fmt.Fprintf(&handoff, "Open tasks: %d\n\n", blocked)
	handoff.WriteString("Next: run `kernel goal status`; regenerate this file with `kernel goal export`.\n")
	handoff.WriteString(GeneratedByTag + "\n")

	return map[string]string{"PROJECT.md": project.String(), "STATE.md": state.String(), "HANDOFF.md": handoff.String()}
}

func hash(files map[string]string) string {
	sum := sha256.New()
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sum.Write([]byte(k))
		sum.Write([]byte{0})
		sum.Write([]byte(files[k]))
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// Write renders the snapshot into <dir>/.project/ atomically.
func Write(dir string, snap Snapshot) error {
	target := filepath.Join(dir, ".project")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	for name, content := range render(snap) {
		if len(content) > MaxFileBytes {
			return fmt.Errorf("%s exceeds %d bytes", name, MaxFileBytes)
		}
		tmp := filepath.Join(target, name+".tmp")
		if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(target, name)); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(target, SnapshotFile+".tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(target, SnapshotFile))
}

// Read returns the stored snapshot, if any.
func Read(dir string) (Snapshot, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ".project", SnapshotFile))
	if os.IsNotExist(err) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, err
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return Snapshot{}, false, err
	}
	return snap, true, nil
}

// Drift compares the stored snapshot with the current database facts.
func Drift(st *store.Store, obj goal.Objective, dir string) (bool, error) {
	stored, ok, err := Read(dir)
	if err != nil || !ok {
		return false, err
	}
	current, err := Build(st, obj, dir)
	if err != nil {
		return false, err
	}
	return stored.Hash != current.Hash, nil
}
