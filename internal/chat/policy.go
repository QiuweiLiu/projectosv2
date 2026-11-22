package chat

// Local approval policy: the first layer decides what is safe in the current
// workspace, what is clearly dangerous, and what only the model (or the
// operator) can judge. It never calls a model.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"projectos/kernel/internal/conversation"
)

type riskDecision int

const (
	riskAllow riskDecision = iota
	riskAskModel
	riskPrompt
)

func (m *Manager) permissionDecision(c conversation.Conversation, action string, resources []string) (riskDecision, string) {
	root := strings.TrimSpace(c.Dir)
	if root == "" {
		root = m.cfg.Root
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "read", "list", "glob", "grep":
		return pathDecision(root, resources)
	case "webfetch":
		return riskPrompt, "网络请求需要人工确认"
	case "edit", "write", "patch", "multiedit":
		return pathDecision(root, resources)
	case "bash", "shell":
		return m.shellDecision(root, strings.Join(resources, " "))
	case "question":
		return riskPrompt, ""
	default:
		return riskAskModel, ""
	}
}

func pathDecision(root string, resources []string) (riskDecision, string) {
	if len(resources) == 0 {
		return riskAskModel, ""
	}
	for _, resource := range resources {
		resolved, err := resolvePath(root, resource)
		if err != nil || !insideRoot(root, resolved) {
			return riskPrompt, "路径在工作区之外，需要人工确认"
		}
		if sensitivePath(resolved) {
			return riskPrompt, "路径包含凭据或版本库内部文件，需要人工确认"
		}
	}
	return riskAllow, ""
}

func resolvePath(root, resource string) (string, error) {
	path := resource
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real, nil
	}
	// A new file has no final component yet; resolve the nearest existing
	// parent and append the remaining relative path.
	parent := filepath.Dir(path)
	for depth := 0; depth < 16; depth++ {
		if real, err := filepath.EvalSymlinks(parent); err == nil {
			rel, rerr := filepath.Rel(parent, path)
			if rerr != nil {
				return "", rerr
			}
			return filepath.Join(real, rel), nil
		}
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		parent = next
	}
	return path, nil
}

func insideRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func sensitivePath(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts {
		lower := strings.ToLower(part)
		switch lower {
		case ".git", ".ssh", ".aws", ".gnupg":
			return true
		}
		base := strings.ToLower(filepath.Base(part))
		if base == ".env" || strings.HasPrefix(base, ".env.") ||
			strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") ||
			base == "id_rsa" || base == "id_ed25519" || base == "credentials" {
			return true
		}
	}
	return false
}

func (m *Manager) shellDecision(root, command string) (riskDecision, string) {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return riskAskModel, ""
	}
	lower := strings.ToLower(cmd)
	for _, bad := range []string{
		"rm -rf", "rm -fr", "rm -r -f", "rm -f -r",
		"git reset --hard", "git push --force", "git push -f", "git clean -fd", "git clean -df",
		"sudo ", "sudo\t", "chmod ", "chown ", "mkfs", "dd if=", "> /dev/",
		"shutdown", "reboot", "curl ", "wget ", "kill ", "pkill ",
	} {
		if strings.Contains(lower, bad) {
			return riskPrompt, "命令包含高风险操作，需要人工确认"
		}
	}
	// Do not try to parse shell syntax we cannot model safely.
	if strings.ContainsAny(cmd, ">`") || strings.Contains(cmd, "$(") || strings.Contains(cmd, "${") ||
		strings.Contains(cmd, "\n") || strings.HasPrefix(lower, "if ") ||
		strings.HasPrefix(lower, "for ") || strings.HasPrefix(lower, "while ") {
		return riskAskModel, ""
	}
	result := riskAllow
	for _, part := range strings.FieldsFunc(cmd, func(r rune) bool {
		return r == ';' || r == '|' || r == '&'
	}) {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		decision, reason := m.argvDecision(root, fields)
		if decision > result {
			result = decision
		}
		if result == riskPrompt {
			return result, reason
		}
	}
	return result, ""
}

func (m *Manager) argvDecision(root string, fields []string) (riskDecision, string) {
	name := fields[0]
	if strings.EqualFold(name, "sudo") {
		return riskPrompt, "sudo 需要人工确认"
	}
	if strings.Contains(name, "/") {
		return riskAskModel, ""
	}
	// Rules are checked after the hard blacklist below, so a remembered
	// "always allow" can never approve a destructive command.
	for _, arg := range fields[1:] {
		if strings.HasPrefix(arg, "/") || filepath.IsAbs(arg) || strings.Contains(arg, "..") {
			resolved, err := resolvePath(root, arg)
			if err != nil || !insideRoot(root, resolved) || sensitivePath(resolved) {
				return riskPrompt, "命令访问工作区之外的路径，需要人工确认"
			}
		}
		if sensitivePath(arg) {
			return riskPrompt, "命令访问凭据或版本库内部文件，需要人工确认"
		}
	}
	switch name {
	case "rm", "chmod", "chown", "dd", "mkfs", "kill", "pkill", "shutdown", "reboot", "curl", "wget":
		return riskPrompt, "命令包含高风险操作，需要人工确认"
	case "git":
		if len(fields) > 1 {
			switch fields[1] {
			case "reset", "clean", "push":
				for _, arg := range fields[2:] {
					if strings.HasPrefix(arg, "--hard") || strings.HasPrefix(arg, "-f") ||
						strings.HasPrefix(arg, "--force") || strings.HasPrefix(arg, "-d") {
						return riskPrompt, "git 命令包含高风险操作，需要人工确认"
					}
				}
			case "branch":
				for _, arg := range fields[2:] {
					if strings.HasPrefix(arg, "-D") || strings.HasPrefix(arg, "-d") || strings.HasPrefix(arg, "-m") {
						return riskPrompt, "git branch 会修改分支，需要人工确认"
					}
				}
			}
		}
	case "find":
		for _, arg := range fields[1:] {
			if arg == "-exec" || arg == "-execdir" || arg == "-delete" {
				return riskPrompt, "find 会执行或删除文件，需要人工确认"
			}
		}
	case "go":
		for _, arg := range fields[2:] {
			if arg == "-exec" {
				return riskPrompt, "go test -exec 会执行外部程序，需要人工确认"
			}
		}
	case "env":
		if len(fields) > 1 {
			for _, arg := range fields[1:] {
				if strings.Contains(arg, "=") {
					return riskAskModel, ""
				}
			}
		}
	}
	rules := m.approvalRules()
	if ruleMatches(rules, "shell", fields) || ruleMatches(rules, "bash", fields) {
		return riskAllow, ""
	}
	if allowedCommand(fields) {
		return riskAllow, ""
	}
	return riskAskModel, ""
}

func allowedCommand(fields []string) bool {
	name := fields[0]
	switch name {
	case "ls", "pwd", "cat", "head", "tail", "wc", "grep", "rg", "find", "stat", "file", "which", "echo":
		return true
	case "env":
		return len(fields) == 1
	case "git":
		if len(fields) < 2 {
			return false
		}
		switch fields[1] {
		case "status", "diff", "log", "show", "branch", "rev-parse", "add":
			return true
		}
	case "go":
		if len(fields) < 2 {
			return false
		}
		switch fields[1] {
		case "test", "build", "vet", "list", "fmt":
			for _, arg := range fields[2:] {
				if arg == "-exec" {
					return false
				}
			}
			return true
		}
	case "gofmt":
		return true
	case "npm", "pnpm":
		if len(fields) >= 2 && fields[1] == "test" {
			return true
		}
		if len(fields) >= 3 && fields[1] == "run" && (fields[2] == "test" || fields[2] == "build" || fields[2] == "lint") {
			return true
		}
	case "pytest":
		return true
	case "python", "python3":
		return len(fields) >= 3 && fields[1] == "-m" && fields[2] == "pytest"
	case "cargo":
		if len(fields) >= 2 {
			switch fields[1] {
			case "test", "check", "build":
				return true
			}
		}
	}
	return false
}

// approval-rules.json remembers "always allow" prefixes for this workspace.
// The hard blacklist is evaluated before these rules, so they cannot approve
// a destructive command.

type approvalRule struct {
	Action string   `json:"action"`
	Prefix []string `json:"prefix"`
}

type approvalRulesFile struct {
	Allow []approvalRule `json:"allow"`
}

func (m *Manager) approvalRulesPath() string {
	return filepath.Join(m.cfg.Root, "approval-rules.json")
}

func (m *Manager) approvalRules() approvalRulesFile {
	m.rulesMu.Lock()
	defer m.rulesMu.Unlock()
	raw, err := os.ReadFile(m.approvalRulesPath())
	if err != nil {
		return approvalRulesFile{}
	}
	var out approvalRulesFile
	_ = json.Unmarshal(raw, &out)
	return out
}

func ruleMatches(rules approvalRulesFile, action string, fields []string) bool {
	for _, rule := range rules.Allow {
		if !strings.EqualFold(rule.Action, action) || len(rule.Prefix) == 0 || len(rule.Prefix) > len(fields) {
			continue
		}
		match := true
		for i, part := range rule.Prefix {
			if fields[i] != part {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func (m *Manager) recordApprovalRule(action string, resources []string) {
	if !strings.EqualFold(action, "bash") && !strings.EqualFold(action, "shell") {
		return
	}
	fields := strings.Fields(strings.Join(resources, " "))
	if len(fields) == 0 {
		return
	}
	prefix := fields
	if len(prefix) > 3 {
		prefix = prefix[:3]
	}
	m.rulesMu.Lock()
	defer m.rulesMu.Unlock()
	var rules approvalRulesFile
	if raw, err := os.ReadFile(m.approvalRulesPath()); err == nil {
		_ = json.Unmarshal(raw, &rules)
	}
	for _, rule := range rules.Allow {
		if strings.EqualFold(rule.Action, action) && strings.Join(rule.Prefix, "\x00") == strings.Join(prefix, "\x00") {
			return
		}
	}
	rules.Allow = append(rules.Allow, approvalRule{Action: strings.ToLower(action), Prefix: prefix})
	raw, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.approvalRulesPath()), ".approval-*.tmp")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return
	}
	_ = tmp.Close()
	_ = os.Rename(tmpPath, m.approvalRulesPath())
}
