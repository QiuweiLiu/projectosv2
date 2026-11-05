package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"projectos/kernel/internal/domain"
)

// FindingsHash identifies one exact set of review findings. A cached Chinese
// explanation is only valid for the findings it was written from.
func FindingsHash(findings []domain.Finding) string {
	raw, _ := json.Marshal(findings)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

func ExplanationPath(dir string, taskID domain.TaskID) string {
	return filepath.Join(dir, "explain", string(taskID)+".md")
}

// ExplainPrompt asks the reporter to translate findings, not to judge them.
func ExplainPrompt(objective string, findings []domain.Finding) string {
	var b strings.Builder
	b.WriteString(`你是这个项目的中文汇报人。下面是自动审查对某个任务给出的意见，原文是英文。
请把它转述给不写代码的人。

对每一条：
- 一句话说清问题是什么；
- 一句话说清会带来什么后果；
- P0/P1 说清楚需要怎么改；P2/P3 说可以怎么改进。
保留严重级别（P0/P1/P2/P3），不要提代码标识符，不要编造意见里没有的内容。
输出纯文本，每条一段，不要 markdown 标题、不要列表符号。

任务目标：
`)
	b.WriteString(objective)
	b.WriteString("\n\n审查意见：\n")
	for _, f := range findings {
		fmt.Fprintf(&b, "[%s] %s\n", f.Severity, f.Message)
	}
	return b.String()
}

// WriteExplanation stores the text together with the hash it describes.
func WriteExplanation(dir string, taskID domain.TaskID, hash, text string) error {
	path := ExplanationPath(dir, taskID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := "<!-- findings:" + hash + " -->\n" + strings.TrimSpace(text) + "\n"
	return os.WriteFile(path, []byte(body), 0o644)
}

// ReadExplanation returns the cached text only when it matches the findings.
func ReadExplanation(dir string, taskID domain.TaskID, hash string) (string, bool) {
	raw, err := os.ReadFile(ExplanationPath(dir, taskID))
	if err != nil {
		return "", false
	}
	lines := strings.SplitN(string(raw), "\n", 2)
	if len(lines) != 2 || strings.TrimSpace(lines[0]) != "<!-- findings:"+hash+" -->" {
		return "", false
	}
	return strings.TrimSpace(lines[1]), true
}
