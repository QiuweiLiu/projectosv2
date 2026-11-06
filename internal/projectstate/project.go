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
)

// ProjectMeta is the durable identity of a workspace project. It deliberately
// carries no runtime status; status is rebuilt from the objective databases.
type ProjectMeta struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"createdAt,omitempty"`
}

// ProjectObjective is one objective's verified facts, already reduced by the
// caller. It must not contain raw logs or unverified model claims.
type ProjectObjective struct {
	ID          string    `json:"id"`
	Title       string    `json:"title,omitempty"`
	Goal        string    `json:"goal"`
	Status      string    `json:"status"`
	PlanVersion int       `json:"planVersion"`
	Accepted    int       `json:"accepted"`
	Total       int       `json:"total"`
	Delivery    bool      `json:"delivery"`
	Blockers    []string  `json:"blockers,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type ProjectSnapshot struct {
	ProjectID   string             `json:"projectId"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Objectives  []ProjectObjective `json:"objectives"`
	Verified    []string           `json:"verified,omitempty"`
	Blockers    []string           `json:"blockers,omitempty"`
	Decisions   []string           `json:"decisions,omitempty"`
	GeneratedAt time.Time          `json:"generatedAt"`
	Hash        string             `json:"hash"`
}

const (
	// A project view is larger than one objective view but still bounded. The
	// limit is deliberately explicit; it is not a log file.
	ProjectMaxFileBytes  = 8192
	projectMaxObjectives = 8
	projectMaxVerified   = 12
	projectMaxBlockers   = 6
	projectMaxDecisions  = 3
	projectClipRunes     = 80
)

// ProjectDir is the project-level state directory. It is intentionally outside
// projects/<objective>/ so an objective snapshot and a project snapshot cannot
// be mistaken for one another.
func ProjectDir(root, projectID string) string {
	return filepath.Join(root, "project-state", projectID, ".project")
}

func BuildProject(meta ProjectMeta, objectives []ProjectObjective, decisions []string) ProjectSnapshot {
	snap := ProjectSnapshot{
		ProjectID: meta.ID, Name: meta.Name, Description: clip(meta.Description, 400),
		GeneratedAt: time.Now().UTC(),
	}
	sort.Slice(objectives, func(i, j int) bool { return objectives[i].UpdatedAt.After(objectives[j].UpdatedAt) })
	if len(objectives) > projectMaxObjectives {
		objectives = objectives[:projectMaxObjectives]
	}
	for _, obj := range objectives {
		obj.Title = clip(obj.Title, projectClipRunes/2)
		obj.Goal = clip(obj.Goal, projectClipRunes)
		for i := range obj.Blockers {
			obj.Blockers[i] = clip(obj.Blockers[i], projectClipRunes)
		}
		snap.Objectives = append(snap.Objectives, obj)
		for _, b := range obj.Blockers {
			if len(snap.Blockers) >= projectMaxBlockers {
				break
			}
			snap.Blockers = append(snap.Blockers, obj.ID+": "+b)
		}
	}
	for _, d := range decisions {
		if len(snap.Decisions) >= projectMaxDecisions {
			break
		}
		if strings.TrimSpace(d) != "" {
			snap.Decisions = append(snap.Decisions, clip(strings.TrimSpace(d), projectClipRunes+20))
		}
	}
	snap.Hash = projectHash(snap)
	return snap
}

// AddVerified records one delivered artifact as a project-level verified fact.
func (s *ProjectSnapshot) AddVerified(objectiveID, taskID, artifact string) {
	if len(s.Verified) >= projectMaxVerified {
		return
	}
	s.Verified = append(s.Verified, fmt.Sprintf("%s/%s %s", objectiveID, taskID, clip(artifact, projectClipRunes)))
	s.Hash = projectHash(*s)
}

func projectHash(s ProjectSnapshot) string {
	h := sha256.New()
	h.Write([]byte(s.ProjectID))
	h.Write([]byte{0})
	h.Write([]byte(s.Name))
	h.Write([]byte{0})
	for _, obj := range s.Objectives {
		h.Write([]byte(obj.ID))
		h.Write([]byte{0})
		h.Write([]byte(obj.Status))
		h.Write([]byte{0})
		fmt.Fprintf(h, "%d/%d", obj.Accepted, obj.Total)
		h.Write([]byte{0})
		for _, b := range obj.Blockers {
			h.Write([]byte(b))
			h.Write([]byte{0})
		}
	}
	for _, v := range s.Verified {
		h.Write([]byte(v))
		h.Write([]byte{0})
	}
	for _, d := range s.Decisions {
		h.Write([]byte(d))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func renderProject(s ProjectSnapshot) map[string]string {
	var project strings.Builder
	project.WriteString("# Project\n\n")
	fmt.Fprintf(&project, "Name: %s\n", s.Name)
	if s.Description != "" {
		fmt.Fprintf(&project, "\n%s\n", s.Description)
	}
	project.WriteString("\n## Objectives\n\n")
	if len(s.Objectives) == 0 {
		project.WriteString("No objectives yet.\n")
	}
	for i, obj := range s.Objectives {
		if i >= 12 {
			project.WriteString("\n… more objectives omitted\n")
			break
		}
		fmt.Fprintf(&project, "- %s %s (%s)\n", obj.ID, obj.Title, obj.Status)
	}
	project.WriteString("\n## Verified outcomes\n\n")
	if len(s.Verified) == 0 {
		project.WriteString("None yet.\n")
	}
	for i, v := range s.Verified {
		if i >= 20 {
			break
		}
		fmt.Fprintf(&project, "- %s\n", v)
	}
	project.WriteString("\n" + GeneratedByTag + "\n")

	var state strings.Builder
	state.WriteString("# State\n\n")
	state.WriteString("| Objective | Status | Plan | Accepted | Delivery |\n|---|---|---|---|---|\n")
	for i, obj := range s.Objectives {
		if i >= 20 {
			break
		}
		fmt.Fprintf(&state, "| %s | %s | v%d | %d/%d | %v |\n",
			obj.ID, obj.Status, obj.PlanVersion, obj.Accepted, obj.Total, obj.Delivery)
	}
	state.WriteString("\n## Blockers\n\n")
	if len(s.Blockers) == 0 {
		state.WriteString("None.\n")
	}
	for i, b := range s.Blockers {
		if i >= 8 {
			break
		}
		fmt.Fprintf(&state, "- %s\n", b)
	}
	state.WriteString("\n" + GeneratedByTag + "\n")

	var handoff strings.Builder
	handoff.WriteString("# Handoff\n\n")
	active := 0
	for _, obj := range s.Objectives {
		if obj.Status == "active" || obj.Status == "planned" || obj.Status == "draft" {
			active++
		}
	}
	fmt.Fprintf(&handoff, "Active objectives: %d\n", active)
	fmt.Fprintf(&handoff, "Blocked objectives: %d\n", len(s.Blockers))
	if len(s.Blockers) > 0 {
		handoff.WriteString("\n## Current blockers\n\n")
		for i, b := range s.Blockers {
			if i >= 6 {
				break
			}
			fmt.Fprintf(&handoff, "- %s\n", b)
		}
	}
	handoff.WriteString("\nNext: inspect active objectives in the cockpit; this file is regenerated from databases.\n")
	handoff.WriteString(GeneratedByTag + "\n")

	var decisions strings.Builder
	decisions.WriteString("# Decisions\n\n")
	if len(s.Decisions) == 0 {
		decisions.WriteString("No project-level decisions recorded.\n")
	}
	for i, d := range s.Decisions {
		if i >= 6 {
			break
		}
		fmt.Fprintf(&decisions, "- %s\n", d)
	}
	decisions.WriteString("\n" + GeneratedByTag + "\n")

	return map[string]string{
		"PROJECT.md": project.String(), "STATE.md": state.String(),
		"HANDOFF.md": handoff.String(), "DECISIONS.md": decisions.String(),
	}
}

// WriteProject renders the project view atomically. Every file is bounded; a
// project with too many objectives is truncated at render time, not silently
// allowed to grow.
func WriteProject(root string, snap ProjectSnapshot) error {
	target := ProjectDir(root, snap.ProjectID)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	for name, content := range renderProject(snap) {
		if len(content) > ProjectMaxFileBytes {
			return fmt.Errorf("project %s exceeds %d bytes", name, ProjectMaxFileBytes)
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

func ReadProject(root, projectID string) (ProjectSnapshot, bool, error) {
	raw, err := os.ReadFile(filepath.Join(ProjectDir(root, projectID), SnapshotFile))
	if os.IsNotExist(err) {
		return ProjectSnapshot{}, false, nil
	}
	if err != nil {
		return ProjectSnapshot{}, false, err
	}
	var snap ProjectSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return ProjectSnapshot{}, false, err
	}
	return snap, true, nil
}

func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
