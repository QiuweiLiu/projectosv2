// Package delivery produces the structured hand-off for a finished objective.
package delivery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
	"projectos/kernel/internal/verify"
)

type TaskReport struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"`
	Attempts int      `json:"attempts"`
	Artifact string   `json:"artifact,omitempty"`
	Error    string   `json:"error,omitempty"`
	Versions []string `json:"artifactVersions,omitempty"`
}

type Check struct {
	TaskID  string `json:"task"`
	Command string `json:"command"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

type Report struct {
	Objective  string       `json:"objective"`
	Goal       string       `json:"goal"`
	Status     string       `json:"status"`
	Plan       int          `json:"planVersion"`
	Tasks      []TaskReport `json:"tasks"`
	Checks     []Check      `json:"checks,omitempty"`
	Acceptance string       `json:"acceptance,omitempty"`
	Open       []string     `json:"open,omitempty"`
	Generated  time.Time    `json:"generatedAt"`
}

func Build(st *store.Store, obj goal.Objective) (Report, error) {
	tasks, err := st.List()
	if err != nil {
		return Report{}, err
	}
	report := Report{Objective: obj.Title, Goal: obj.Goal, Status: string(obj.Status),
		Plan: obj.PlanVersion, Acceptance: obj.Acceptance, Generated: time.Now().UTC()}
	for _, t := range tasks {
		row := TaskReport{ID: string(t.ID), Status: string(t.Status), Attempts: t.Attempts,
			Artifact: t.ArtifactPath, Error: t.LastError}
		evidence, err := st.EvidenceFor(t.ID)
		if err != nil {
			return Report{}, err
		}
		for _, e := range evidence {
			row.Versions = append(row.Versions, e.ArtifactVersion)
		}
		if t.Status != domain.TaskAccepted {
			report.Open = append(report.Open, fmt.Sprintf("%s: %s (%s)", t.ID, t.Status, t.LastError))
		}
		report.Tasks = append(report.Tasks, row)
	}
	return report, nil
}

func Write(dir string, r Report) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "delivery.json"), raw, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "delivery.md"), []byte(markdown(r)), 0o644)
}

func markdown(r Report) string {
	out := "# 交付报告\n\n"
	out += fmt.Sprintf("- 目标：%s\n- 状态：%s\n- 计划版本：v%d\n- 生成时间：%s\n\n", r.Objective, r.Status, r.Plan, r.Generated.Format(time.RFC3339))
	out += "## 任务\n\n| Task | 状态 | 尝试 | 产物 | 说明 |\n|---|---|---|---|---|\n"
	for _, t := range r.Tasks {
		out += fmt.Sprintf("| %s | %s | %d | %s | %s |\n", t.ID, t.Status, t.Attempts, t.Artifact, t.Error)
	}
	if len(r.Checks) > 0 {
		out += "\n## 交付验收（在 out/ 的副本上运行）\n\n| Task | 命令 | 结果 |\n|---|---|---|\n"
		for _, c := range r.Checks {
			result := "通过"
			if !c.OK {
				result = "失败：" + strings.ReplaceAll(c.Message, "|", "/")
			}
			out += fmt.Sprintf("| %s | `%s` | %s |\n", c.TaskID, c.Command, result)
		}
	}
	if len(r.Open) > 0 {
		out += "\n## 未完成\n\n"
		for _, o := range r.Open {
			out += "- " + o + "\n"
		}
	}
	out += "\n## 说明\n\n报告由 Kernel 依据运行记录生成；Worker 的自我描述不构成验收证据。\n"
	return out
}

// Assemble copies every accepted artifact into <dir>/out so the delivered tree
// is usable as a whole, not just as per-task fragments.
func Assemble(st *store.Store, dir string) (int, error) {
	tasks, err := st.List()
	if err != nil {
		return 0, err
	}
	root := filepath.Join(dir, "out")
	if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return 0, fmt.Errorf("%s is a symlink; refusing to assemble through it", root)
	}
	count := 0
	for _, t := range tasks {
		if t.Status != domain.TaskAccepted || t.ArtifactPath == "" {
			continue
		}
		content, err := st.AcceptedResult(t.ID)
		if err != nil {
			return count, fmt.Errorf("task %s has no accepted artifact: %w", t.ID, err)
		}
		clean := filepath.Clean(t.ArtifactPath)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return count, fmt.Errorf("task %s artifact escapes the tree: %q", t.ID, t.ArtifactPath)
		}
		target := filepath.Join(root, clean)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return count, err
		}
		if err := noSymlinkPath(root, clean); err != nil {
			return count, err
		}
		// The final path may itself be a symlink; O_NOFOLLOW makes the write
		// fail instead of overwriting the link target outside out/.
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
		if err != nil {
			return count, fmt.Errorf("refusing to write %s: %w", clean, err)
		}
		if _, err := file.WriteString(content); err != nil {
			file.Close()
			return count, err
		}
		if err := file.Close(); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// VerifyAssembled runs every accepted task's command validators against a copy
// of the delivered tree. Per-task sandboxes prove each file on its own; this
// proves the assembled set actually works together, without mutating out/.
func VerifyAssembled(st *store.Store, dir string) ([]Check, error) {
	source := filepath.Join(dir, "out")
	if _, err := os.Stat(source); err != nil {
		return nil, nil
	}
	work, err := os.MkdirTemp("", "kernel-delivery-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	if err := copyTree(source, work); err != nil {
		return nil, err
	}
	tasks, err := st.List()
	if err != nil {
		return nil, err
	}
	var checks []Check
	for _, t := range tasks {
		if t.Status != domain.TaskAccepted {
			continue
		}
		for _, spec := range t.Commands {
			check := Check{TaskID: string(t.ID), Command: spec.Command, OK: true}
			if err := verify.RunCommand(work, spec); err != nil {
				check.OK = false
				check.Message = err.Error()
			}
			checks = append(checks, check)
		}
	}
	return checks, nil
}

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("delivery tree contains a symlink: %s", rel)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	})
}

// noSymlinkPath refuses to write through a symlinked directory. The path check
// keeps artifacts inside the tree textually; this keeps them inside physically.
func noSymlinkPath(root, rel string) error {
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	path := root
	for _, part := range parts[:len(parts)-1] {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to write through symlink %s", path)
		}
	}
	return nil
}
