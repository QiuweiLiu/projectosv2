// Package report turns kernel state into a short briefing for a human. It never
// decides anything: it only describes what the database already proves.
package report

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

type Brief struct {
	Status   string   `json:"status"`   // one line
	Progress string   `json:"progress"` // one line
	Done     []string `json:"done"`
	Doing    []string `json:"doing"`
	Blocked  []string `json:"blocked"`
	Next     string   `json:"next"`
}

var statusLabel = map[domain.TaskStatus]string{
	domain.TaskPending:    "待执行",
	domain.TaskRunning:    "执行中",
	domain.TaskAccepted:   "已通过",
	domain.TaskBlocked:    "被卡住",
	domain.TaskRejected:   "未通过",
	domain.TaskSuperseded: "已被替换",
}

// humanReason turns the kernel's machine reasons into something readable.
func humanReason(text string) string {
	switch {
	case strings.HasPrefix(text, "review: blocking finding"):
		return "审查发现了需要修的问题"
	case strings.HasPrefix(text, "acceptance:"):
		return "验收判断没有完成度达标"
	case strings.HasPrefix(text, "dependency blocked"):
		return "上游任务没有完成"
	case strings.HasPrefix(text, "interrupted: outcome unknown"):
		return "执行进程中断，结果未知"
	case strings.HasPrefix(text, "cancelled:"), strings.HasPrefix(text, "cancelled "):
		return "已取消"
	case strings.HasPrefix(text, "verification: command"):
		if i := strings.Index(text, "failed:"); i > 0 {
			return "验收命令执行失败" + clip(text[i+len("failed:"):], 120)
		}
		return "验收命令执行失败"
	case strings.HasPrefix(text, "verification:"):
		return "产物内容不符合要求"
	case strings.HasPrefix(text, "worker failed"):
		return "执行器报告失败"
	case strings.HasPrefix(text, "run timeout"):
		return "执行超时"
	case strings.HasPrefix(text, "budget"):
		return "预算用完"
	default:
		return clip(text, 140)
	}
}

func clip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func Build(st *store.Store, obj goal.Objective) (Brief, error) {
	tasks, err := st.List()
	if err != nil {
		return Brief{}, err
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })

	brief := Brief{}
	accepted, running, pending, blocked, superseded := 0, 0, 0, 0, 0
	for _, task := range tasks {
		if task.Status == domain.TaskSuperseded {
			superseded++
			continue
		}
		label := statusLabel[task.Status]
		name := filepath.Base(task.ArtifactPath)
		if name == "" {
			name = string(task.ID)
		}
		line := fmt.Sprintf("%s（%s）%s", task.ID, name, label)
		switch task.Status {
		case domain.TaskAccepted:
			accepted++
			brief.Done = append(brief.Done, line)
		case domain.TaskRunning:
			running++
			brief.Doing = append(brief.Doing, line)
		case domain.TaskPending:
			pending++
		case domain.TaskBlocked, domain.TaskRejected:
			blocked++
			reason := humanReason(task.LastError)
			if task.LastError != "" {
				line += "：" + reason
			}
			brief.Blocked = append(brief.Blocked, line)
		}
	}
	total := accepted + running + pending + blocked + superseded
	brief.Progress = fmt.Sprintf("共 %d 个任务，%d 个已通过，%d 个执行中，%d 个待执行，%d 个被卡住。",
		total, accepted, running, pending, blocked)
	if superseded > 0 {
		brief.Progress += fmt.Sprintf("另有 %d 个旧任务已被替换。", superseded)
	}
	switch {
	case obj.Status == goal.StatusCompleted:
		brief.Status = "已完成：所有任务通过验收，交付已生成。"
		brief.Next = "查看 out/ 目录或 delivery.md 了解交付内容。"
	case obj.Status == goal.StatusFailed:
		brief.Status = "未能完成：有任务被卡住，且没有新的替代计划。"
		brief.Next = "看下面的「被卡住」，修好后再运行；或取消目标。"
	case obj.Status == goal.StatusCancelled:
		brief.Status = "已取消。"
		brief.Next = "需要的话可以新建一个目标。"
	case blocked > 0:
		brief.Status = "执行中，但有任务被卡住。"
		brief.Next = "系统会尝试一次有界重规划；也可以取消后重新发起。"
	case running > 0:
		brief.Status = "正在执行。"
		brief.Next = "等待当前任务完成；验收通过后会自动继续下游任务。"
	default:
		brief.Status = "等待执行。"
		brief.Next = "确认计划后开始执行。"
	}
	return brief, nil
}

// Render writes the brief as plain text suitable for a UI card.
func Render(b Brief) string {
	var out strings.Builder
	out.WriteString(b.Status + "\n")
	out.WriteString(b.Progress + "\n")
	if len(b.Done) > 0 {
		out.WriteString("\n已通过：\n")
		for _, line := range b.Done {
			out.WriteString("· " + line + "\n")
		}
	}
	if len(b.Doing) > 0 {
		out.WriteString("\n正在做：\n")
		for _, line := range b.Doing {
			out.WriteString("· " + line + "\n")
		}
	}
	if len(b.Blocked) > 0 {
		out.WriteString("\n被卡住：\n")
		for _, line := range b.Blocked {
			out.WriteString("· " + line + "\n")
		}
	}
	if b.Next != "" {
		out.WriteString("\n下一步：" + b.Next + "\n")
	}
	return strings.TrimSpace(out.String())
}

// Summarizer is the model call the reporter uses when asked to rewrite the
// deterministic brief. It has no authority over task state.
type Summarizer interface {
	Summarize(ctx context.Context, prompt string) (string, error)
}

// Polish asks the model to rewrite the facts as a short briefing for a human.
// The facts are given verbatim; the model is told not to invent.
func Polish(ctx context.Context, s Summarizer, facts string) (string, error) {
	prompt := `你是这个项目的中文汇报人。把下面的系统状态改写成 3 到 6 句中文汇报，读者是不写代码的人。

要求：
- 只陈述下面事实里有的事，不猜测、不承诺、不补充。
- 不要出现哈希、文件路径、任务 ID、英文状态词。
- 不要 markdown 表格或标题，就用自然段。
- 如果有被卡住的任务，用一句话说明卡在哪、下一步该怎么办。
- 语气平实，不要"我们"、不要感叹号。

系统状态：
` + facts
	return s.Summarize(ctx, prompt)
}

// StateHash identifies the exact state a model report describes. A cached report
// from an earlier plan version must not be shown as if it were current.
func StateHash(obj goal.Objective, tasks []domain.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "obj:%s|%s|%d\n", obj.ID, obj.Status, obj.PlanVersion)
	sorted := make([]domain.Task, len(tasks))
	copy(sorted, tasks)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, t := range sorted {
		fmt.Fprintf(&b, "%s:%s:%d:%d:%s\n", t.ID, t.Status, t.Attempts, t.PlanVersion, t.ArtifactPath)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

func ModelReportPath(dir string) string { return filepath.Join(dir, "report.md") }

func WriteModelReport(dir, hash, text string) error {
	body := "<!-- state:" + hash + " -->\n" + strings.TrimSpace(text) + "\n"
	return os.WriteFile(ModelReportPath(dir), []byte(body), 0o644)
}

func ReadModelReport(dir, hash string) (string, bool) {
	raw, err := os.ReadFile(ModelReportPath(dir))
	if err != nil {
		return "", false
	}
	lines := strings.SplitN(string(raw), "\n", 2)
	if len(lines) != 2 || strings.TrimSpace(lines[0]) != "<!-- state:"+hash+" -->" {
		return "", false
	}
	return strings.TrimSpace(lines[1]), true
}
