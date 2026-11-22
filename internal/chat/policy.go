package chat

// Local approval policy: the first layer decides what is safe in the current
// workspace, what is clearly dangerous, and what only the model (or the
// operator) can judge. It never calls a model.

import (
	"encoding/json"
	"fmt"
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

const maxSymlinkDepth = 16

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

// resolvePath follows symlinks for the file itself, or for its nearest
// existing parent when the file does not exist yet. It fails closed when that
// walk exceeds maxSymlinkDepth: returning the unresolved path would let a
// symlinked parent escape the workspace check.
//
// The raw path is never cleaned before EvalSymlinks: for "link/../passwd" the
// kernel resolves link first and then .. from the link target, while lexical
// cleaning would pretend the result is still inside the workspace.
func resolvePath(root, resource string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("empty workspace root")
	}
	path := resource
	if !filepath.IsAbs(path) {
		path = strings.TrimRight(root, string(filepath.Separator)) + string(filepath.Separator) + resource
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real, nil
	}
	// The full path does not exist. Resolve the longest existing prefix with
	// the same raw semantics and append the missing suffix. The first missing
	// component cannot be a symlink yet, so lexical cleanup of the suffix is
	// safe.
	sep := string(filepath.Separator)
	prefix := path
	suffix := ""
	for depth := 0; depth < maxSymlinkDepth; depth++ {
		if prefix == "" || prefix == sep {
			break
		}
		slash := strings.LastIndex(prefix, sep)
		var component string
		if slash < 0 {
			component, prefix = prefix, ""
		} else {
			component, prefix = prefix[slash+1:], prefix[:slash]
			if prefix == "" {
				prefix = sep
			}
		}
		if component == "" {
			continue
		}
		if suffix == "" {
			suffix = component
		} else {
			suffix = component + sep + suffix
		}
		if real, err := filepath.EvalSymlinks(prefix); err == nil {
			if hasParentComponent(suffix) {
				// A missing component followed by ".." can become a symlink
				// traversal once a write creates that component.
				return "", fmt.Errorf("cannot resolve %q through missing parent components", resource)
			}
			return filepath.Join(real, suffix), nil
		}
	}
	return "", fmt.Errorf("cannot resolve %q within %d parent levels", resource, maxSymlinkDepth)
}

func hasParentComponent(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func insideRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func sensitivePath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		lower := strings.ToLower(part)
		switch lower {
		case ".git", ".ssh", ".aws", ".gnupg", ".kube", ".docker":
			return true
		case ".env", ".envrc", ".npmrc", ".pypirc", ".netrc", ".git-credentials",
			".htpasswd", ".pgpass", ".my.cnf", ".dockercfg", ".credentials",
			"credentials", "credentials.json", "control-token",
			"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519":
			return true
		}
		if strings.HasPrefix(lower, ".env.") ||
			strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key") ||
			strings.HasSuffix(lower, ".p12") || strings.HasSuffix(lower, ".pfx") ||
			strings.HasSuffix(lower, ".jks") || strings.HasSuffix(lower, ".keystore") ||
			strings.HasSuffix(lower, ".kdbx") || strings.HasSuffix(lower, "_credentials.json") ||
			(strings.HasPrefix(lower, "service-account") && strings.HasSuffix(lower, ".json")) ||
			secretDataFile(lower) {
			return true
		}
	}
	return false
}

// secretDataFile covers secrets/credentials data files without blocking source
// files such as secrets.go or credentials_test.go.
func secretDataFile(lower string) bool {
	for _, prefix := range []string{"secrets", "credentials"} {
		if !strings.HasPrefix(lower, prefix+".") {
			continue
		}
		switch strings.TrimPrefix(lower, prefix+".") {
		case "json", "yaml", "yml", "toml", "env", "ini", "conf", "cfg", "txt":
			return true
		}
	}
	return false
}

// splitShell parses the subset of shell syntax the local policy can model.
// Quoting and backslash escapes are honoured. It returns ok=false instead of
// guessing when the command uses redirection, command/parameter substitution,
// background jobs, subshells, brace/tilde/glob expansion, or unbalanced
// quotes; the caller then asks the model rather than auto-approving.
func splitShell(command string) ([][]string, bool) {
	var (
		commands [][]string
		argv     []string
		token    strings.Builder
		started  bool
		quote    rune
		escaped  bool
	)
	flushToken := func() {
		if started {
			argv = append(argv, token.String())
			token.Reset()
			started = false
		}
	}
	flushCommand := func() {
		flushToken()
		if len(argv) > 0 {
			commands = append(commands, argv)
			argv = nil
		}
	}
	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if escaped {
			if r == '\n' {
				// Backslash-newline is a line continuation: the shell removes
				// both characters, so it must not glue tokens together either.
				escaped = false
				continue
			}
			token.WriteRune(r)
			started = true
			escaped = false
			continue
		}
		switch quote {
		case '\'':
			if r == '\'' {
				quote = 0
			} else {
				token.WriteRune(r)
				started = true
			}
			continue
		case '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			case '$', '`':
				return nil, false
			default:
				token.WriteRune(r)
				started = true
			}
			continue
		}
		switch r {
		case '\\':
			escaped = true
		case '\'', '"':
			quote = r
			started = true
		case ' ', '\t', '\r':
			flushToken()
		case '\n', ';':
			flushCommand()
		case '|':
			if i+1 < len(runes) && runes[i+1] == '|' {
				i++
			}
			flushCommand()
		case '&':
			if i+1 < len(runes) && runes[i+1] == '&' {
				i++
				flushCommand()
			} else {
				return nil, false
			}
		case '#':
			if started {
				token.WriteRune(r)
				continue
			}
			// A comment runs to the end of the line; the newline itself stays
			// for the loop so it still separates commands.
			for i+1 < len(runes) && runes[i+1] != '\n' {
				i++
			}
		case '<', '>', '$', '`', '(', ')', '*', '?', '[':
			return nil, false
		case '{':
			// "{}" (find -exec) and "{a}" are literal in the shell; only a
			// comma or range makes this a brace expansion.
			if braceExpands(runes, i) {
				return nil, false
			}
			token.WriteRune(r)
			started = true
		case '}':
			token.WriteRune(r)
			started = true
		case '~':
			// Tilde expands only at the start of a word.
			if !started {
				return nil, false
			}
			token.WriteRune(r)
		default:
			token.WriteRune(r)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, false
	}
	flushCommand()
	return commands, true
}

// braceExpands reports whether the brace group starting at start contains a
// comma or ".." range and therefore expands into several words.
func braceExpands(runes []rune, start int) bool {
	depth := 0
	for i := start; i < len(runes); i++ {
		switch runes[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return false
			}
		case ',', '\'', '"', '\\':
			return true
		case ' ', '\t', '\r', '\n', ';', '|', '&':
			return false
		case '.':
			if i+1 < len(runes) && runes[i+1] == '.' {
				return true
			}
		}
	}
	return false
}

var shellControlKeywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true,
	"for": true, "while": true, "until": true, "do": true, "done": true,
	"case": true, "esac": true, "function": true, "select": true,
	"time": true, "coproc": true,
}

func (m *Manager) shellDecision(root, command string) (riskDecision, string) {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return riskAskModel, ""
	}
	commands, ok := splitShell(cmd)
	if !ok || len(commands) == 0 {
		return riskAskModel, ""
	}
	result := riskAllow
	for _, fields := range commands {
		if len(fields) == 0 {
			continue
		}
		if shellControlKeywords[strings.ToLower(fields[0])] {
			return riskAskModel, ""
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
	if len(fields) == 0 {
		return riskAskModel, ""
	}
	// Callers may pass either the configured root or an already-resolved one;
	// canonicalize so resolvePath's EvalSymlinks result compares correctly.
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	name := strings.ToLower(fields[0])
	if strings.EqualFold(name, "sudo") {
		return riskPrompt, "sudo 需要人工确认"
	}
	// Executables addressed by path are not matched to the command allowlist:
	// "./ls" could be an arbitrary script.
	if strings.Contains(name, "/") {
		return riskAskModel, ""
	}
	endOfFlags := false
	for _, arg := range fields[1:] {
		if !endOfFlags && arg == "--" {
			endOfFlags = true
			continue
		}
		if decision, reason := checkCommandArg(root, arg, endOfFlags); decision != riskAllow {
			return decision, reason
		}
	}
	// File-reading and formatting commands may receive a bare name ("cat
	// secret", a symlink, ".ENV"). Every operand is resolved, not only
	// path-looking ones. After "--" even a leading dash is part of the name.
	if pathOperandCommand(fields) {
		endOfFlags = false
		for _, arg := range fields[1:] {
			if !endOfFlags && arg == "--" {
				endOfFlags = true
				continue
			}
			if arg == "" || (!endOfFlags && strings.HasPrefix(arg, "-")) {
				continue
			}
			resolved, err := resolvePath(root, arg)
			if err != nil || !insideRoot(root, resolved) {
				return riskPrompt, "命令访问工作区之外的路径，需要人工确认"
			}
			if sensitivePath(resolved) {
				return riskPrompt, "命令访问凭据或版本库内部文件，需要人工确认"
			}
		}
	}
	switch name {
	case "rm", "chmod", "chown", "dd", "mkfs", "kill", "pkill", "shutdown", "reboot", "curl", "wget":
		return riskPrompt, "命令包含高风险操作，需要人工确认"
	case "git":
		if subIndex, sub := gitSubcommand(fields); sub != "" {
			switch sub {
			case "reset", "clean", "push":
				for _, arg := range fields[subIndex+1:] {
					lower := strings.ToLower(arg)
					if strings.HasPrefix(lower, "--hard") || strings.HasPrefix(lower, "-f") ||
						strings.HasPrefix(lower, "--force") || strings.HasPrefix(lower, "-d") {
						return riskPrompt, "git 命令包含高风险操作，需要人工确认"
					}
				}
			case "branch":
				for _, arg := range fields[subIndex+1:] {
					if branchUnsafeFlag(arg) {
						return riskPrompt, "git branch 会修改分支，需要人工确认"
					}
				}
			case "show", "diff":
				args := fields[subIndex+1:]
				for _, arg := range args {
					if !strings.HasPrefix(arg, "-") && strings.Contains(arg, ":(") {
						return riskPrompt, "git " + sub + " 的 pathspec magic 需要人工确认"
					}
				}
				paths := gitPathspecs(args)
				for _, path := range paths {
					if strings.ContainsAny(path, "*?[") {
						return riskPrompt, "git " + sub + " 的 pathspec 含通配符，可能匹配敏感文件，需要人工确认"
					}
					if sensitivePath(path) {
						return riskPrompt, "git " + sub + " 访问凭据或版本库内部文件，需要人工确认"
					}
					resolved, err := resolvePath(root, path)
					if err != nil || !insideRoot(root, resolved) {
						return riskPrompt, "git " + sub + " 访问工作区之外的路径，需要人工确认"
					}
					if info, statErr := os.Stat(resolved); statErr == nil && info.IsDir() {
						return riskPrompt, "git " + sub + " 的目录可能包含敏感文件，需要人工确认"
					}
				}
				if gitPatchFlag(args) {
					return riskPrompt, "git " + sub + " 会输出文件内容，需要人工确认"
				}
				if len(paths) == 0 && !gitSummaryOnly(args) {
					return riskPrompt, "git " + sub + " 默认输出文件内容，需要人工确认"
				}
			case "log":
				if gitPatchFlag(fields[subIndex+1:]) {
					return riskPrompt, "git log 会输出文件内容，需要人工确认"
				}
			}
		}
	case "find":
		for _, arg := range fields[1:] {
			if findUnsafeFlag(arg) {
				return riskPrompt, "find 会执行、写入或删除文件，需要人工确认"
			}
		}
	case "go":
		for _, arg := range fields[1:] {
			if goUnsafeFlag(arg) {
				return riskPrompt, "go 命令会执行外部程序，需要人工确认"
			}
		}
	case "env":
		if envPrintsEnvironment(fields[1:]) {
			return riskPrompt, "环境变量可能包含凭据，需要人工确认"
		}
	case "grep":
		if recursiveGrepFlag(fields[1:]) && grepSearchesRoot(root, fields[1:]) {
			return riskPrompt, "根目录递归搜索可能读取凭据，需要人工确认"
		}
	case "rg":
		if rgUnrestricted(fields[1:]) {
			return riskPrompt, "未限制的递归搜索可能读取凭据，需要人工确认"
		}
	case "bash", "sh", "zsh", "dash", "ksh", "fish":
		if len(fields) > 1 && (fields[1] == "-c" || fields[1] == "--command") {
			return riskPrompt, "解释器内联代码需要人工确认"
		}
	case "python", "python3", "node", "perl", "ruby", "php":
		if len(fields) > 1 {
			switch fields[1] {
			case "-c", "-e", "-E", "--eval", "-p":
				return riskPrompt, "解释器内联代码需要人工确认"
			}
		}
	case "osascript":
		if len(fields) > 1 && (fields[1] == "-e" || fields[1] == "-l") {
			return riskPrompt, "AppleScript 需要人工确认"
		}
	}
	if ruleAllowed(name, fields) {
		rules := m.approvalRules()
		if ruleMatches(rules, "shell", fields) || ruleMatches(rules, "bash", fields) {
			return riskAllow, ""
		}
	}
	if allowedCommand(fields) {
		return riskAllow, ""
	}
	return riskAskModel, ""
}

// checkCommandArg applies the path rules that hold for every command: explicit
// paths must stay inside the workspace, and credential/repository internals are
// always sensitive. --flag=/path values are checked as paths too; otherwise
// "--file=/etc/passwd" would hide the path behind a flag.
func checkCommandArg(root, arg string, literal bool) (riskDecision, string) {
	if !literal && strings.HasPrefix(arg, "-") {
		i := strings.IndexByte(arg, '=')
		if i < 0 || i+1 >= len(arg) {
			if sensitivePath(arg) {
				return riskPrompt, "命令访问凭据或版本库内部文件，需要人工确认"
			}
			return riskAllow, ""
		}
		value := arg[i+1:]
		resolved, err := resolvePath(root, value)
		if err != nil || !insideRoot(root, resolved) {
			return riskPrompt, "命令访问工作区之外的路径，需要人工确认"
		}
		if sensitivePath(resolved) {
			return riskPrompt, "命令访问凭据或版本库内部文件，需要人工确认"
		}
		return riskAllow, ""
	}
	if filepath.IsAbs(arg) || strings.Contains(arg, "/") || strings.Contains(arg, "..") {
		resolved, err := resolvePath(root, arg)
		if err != nil || !insideRoot(root, resolved) {
			return riskPrompt, "命令访问工作区之外的路径，需要人工确认"
		}
		if sensitivePath(resolved) {
			return riskPrompt, "命令访问凭据或版本库内部文件，需要人工确认"
		}
	}
	if sensitivePath(arg) {
		return riskPrompt, "命令访问凭据或版本库内部文件，需要人工确认"
	}
	return riskAllow, ""
}

func pathReadCommand(name string) bool {
	switch name {
	case "ls", "cat", "head", "tail", "wc", "grep", "rg", "find", "stat", "file", "which", "echo":
		return true
	}
	return false
}

// pathWriteCommand formats or rewrites its file operands. A bare symlink must
// be resolved here too, otherwise "gofmt -w link" writes through it.
func pathWriteCommand(name string) bool {
	return name == "gofmt"
}

func pathOperandCommand(fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	name := strings.ToLower(fields[0])
	if pathReadCommand(name) || pathWriteCommand(name) {
		return true
	}
	// Test runners accept file/dir operands (including bare names that may be
	// symlinks). Package patterns and test names resolve inside the root and
	// are harmless to check.
	switch name {
	case "pytest", "python", "python3", "cargo", "go", "npm", "pnpm", "git":
		return true
	}
	return false
}

// envPrintsEnvironment reports whether env has no command operand and would
// only dump the (possibly credential-bearing) environment.
func envPrintsEnvironment(args []string) bool {
	endOfFlags := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !endOfFlags && arg == "--" {
			endOfFlags = true
			continue
		}
		if !endOfFlags && strings.HasPrefix(arg, "-") {
			switch arg {
			case "-u", "--unset", "-C", "--chdir":
				i++
			}
			continue
		}
		if strings.Contains(arg, "=") {
			continue
		}
		return false
	}
	return true
}

func recursiveGrepFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--recursive" || arg == "--dereference-recursive" {
			return true
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") {
			for _, ch := range arg[1:] {
				if ch == 'r' || ch == 'R' {
					return true
				}
			}
		}
	}
	return false
}

// grepSearchesRoot reports whether a recursive grep targets the workspace root
// (or the current directory, which is the root for tool calls). A scoped
// subdirectory is left to the normal path checks.
func grepSearchesRoot(root string, args []string) bool {
	endOfFlags := false
	patternSupplied := false
	var operands []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !endOfFlags && arg == "--" {
			endOfFlags = true
			continue
		}
		if !endOfFlags && (arg == "-e" || arg == "--regexp") {
			patternSupplied = true
			i++
			continue
		}
		if !endOfFlags && (strings.HasPrefix(arg, "--regexp=") ||
			(strings.HasPrefix(arg, "-e") && len(arg) > 2)) {
			patternSupplied = true
			continue
		}
		if !endOfFlags && strings.HasPrefix(arg, "-") {
			continue
		}
		operands = append(operands, arg)
	}
	paths := operands
	if !patternSupplied && len(paths) > 0 {
		paths = paths[1:]
	}
	if len(paths) == 0 {
		return true
	}
	for _, path := range paths {
		resolved, err := resolvePath(root, path)
		if err != nil {
			return true
		}
		if resolved == root {
			return true
		}
	}
	return false
}

func rgUnrestricted(args []string) bool {
	for _, arg := range args {
		if arg == "--follow" || arg == "-L" || arg == "--unrestricted" {
			return true
		}
		if arg == "--hidden" || strings.HasPrefix(arg, "--no-ignore") {
			return true
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") {
			onlyU := len(arg) > 1
			for _, ch := range arg[1:] {
				if ch != 'u' {
					onlyU = false
					break
				}
			}
			if onlyU {
				return true
			}
		}
	}
	return false
}

func gitPathspecs(args []string) []string {
	var paths []string
	endOfFlags := false
	for _, arg := range args {
		if !endOfFlags && arg == "--" {
			endOfFlags = true
			continue
		}
		if !endOfFlags && strings.HasPrefix(arg, "-") {
			continue
		}
		if endOfFlags {
			paths = append(paths, arg)
			continue
		}
		if i := strings.Index(arg, ":"); i >= 0 && i+1 < len(arg) {
			paths = append(paths, arg[i+1:])
		}
	}
	return paths
}

func gitSummaryOnly(args []string) bool {
	for _, arg := range args {
		lower := strings.ToLower(arg)
		switch {
		case lower == "-s", lower == "--no-patch", lower == "--shortstat",
			lower == "--numstat", lower == "--name-only", lower == "--name-status",
			lower == "--summary", lower == "--stat", strings.HasPrefix(lower, "--stat="),
			lower == "--raw", lower == "--check", lower == "--quiet":
			return true
		}
	}
	return false
}

func gitPatchFlag(args []string) bool {
	for _, arg := range args {
		lower := strings.ToLower(arg)
		if lower == "-p" || lower == "--patch" || lower == "-u" ||
			strings.HasPrefix(lower, "--unified") || strings.HasPrefix(arg, "-U") ||
			arg == "-W" || strings.HasPrefix(arg, "-L") || lower == "--function-context" ||
			strings.HasPrefix(lower, "--patch-with") || lower == "--stat-with-patch" ||
			lower == "-c" || lower == "--cc" || strings.HasPrefix(lower, "--diff-merges") ||
			lower == "--remerge-diff" ||
			strings.HasPrefix(lower, "--word-diff") || strings.HasPrefix(lower, "--color-words") {
			return true
		}
	}
	return false
}

func goUnsafeFlag(arg string) bool {
	lower := strings.ToLower(arg)
	return lower == "-exec" || lower == "-toolexec" ||
		strings.HasPrefix(lower, "-exec=") || strings.HasPrefix(lower, "-toolexec=")
}

func findUnsafeFlag(arg string) bool {
	switch strings.ToLower(arg) {
	case "-exec", "-execdir", "-ok", "-okdir", "-delete",
		"-fprint", "-fprint0", "-fprintf", "-fls":
		return true
	}
	return false
}

func branchUnsafeFlag(arg string) bool {
	lower := strings.ToLower(arg)
	if strings.HasPrefix(lower, "--delete") || strings.HasPrefix(lower, "--move") ||
		strings.HasPrefix(lower, "--force") {
		return true
	}
	if strings.HasPrefix(lower, "-") && !strings.HasPrefix(lower, "--") {
		for _, ch := range lower[1:] {
			switch ch {
			case 'd', 'm', 'f':
				return true
			}
		}
	}
	return false
}

// gitSubcommand finds the subcommand after git's global options. Options such
// as -C, -c and --git-dir consume a separate value, so the token after them is
// not the subcommand.
func gitSubcommand(fields []string) (int, string) {
	for i := 1; i < len(fields); i++ {
		arg := fields[i]
		if !strings.HasPrefix(arg, "-") {
			return i, strings.ToLower(arg)
		}
		if strings.Contains(arg, "=") {
			continue
		}
		switch arg {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env":
			i++
		}
	}
	return 0, ""
}

func allowedCommand(fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	name := strings.ToLower(fields[0])
	switch name {
	case "ls", "pwd", "cat", "head", "tail", "wc", "grep", "rg", "find", "stat", "file", "which", "echo":
		if name == "find" {
			for _, arg := range fields[1:] {
				if findUnsafeFlag(arg) {
					return false
				}
			}
		}
		return true
	case "git":
		if len(fields) < 2 {
			return false
		}
		switch strings.ToLower(fields[1]) {
		case "status", "diff", "log", "show", "branch", "rev-parse", "add":
			if strings.EqualFold(fields[1], "branch") {
				for _, arg := range fields[2:] {
					if branchUnsafeFlag(arg) {
						return false
					}
				}
			}
			return true
		}
	case "go":
		if len(fields) < 2 {
			return false
		}
		switch strings.ToLower(fields[1]) {
		case "test", "build", "vet", "list", "fmt":
			for _, arg := range fields[2:] {
				if goUnsafeFlag(arg) {
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

// ApprovalRule is one remembered "always allow" command prefix.
type ApprovalRule struct {
	Action string   `json:"action"`
	Prefix []string `json:"prefix"`
}

type approvalRulesFile struct {
	Allow []ApprovalRule `json:"allow"`
}

func (m *Manager) approvalRulesPath() string {
	return filepath.Join(m.cfg.Root, "approval-rules.json")
}

func (m *Manager) readApprovalRulesLocked() approvalRulesFile {
	raw, err := os.ReadFile(m.approvalRulesPath())
	if err != nil {
		return approvalRulesFile{}
	}
	var out approvalRulesFile
	_ = json.Unmarshal(raw, &out)
	return out
}

func (m *Manager) approvalRules() approvalRulesFile {
	m.rulesMu.Lock()
	defer m.rulesMu.Unlock()
	return m.readApprovalRulesLocked()
}

// ApprovalRules returns a copy for the settings UI.
func (m *Manager) ApprovalRules() []ApprovalRule {
	m.rulesMu.Lock()
	defer m.rulesMu.Unlock()
	rules := m.readApprovalRulesLocked()
	out := make([]ApprovalRule, 0, len(rules.Allow))
	for _, rule := range rules.Allow {
		out = append(out, ApprovalRule{Action: rule.Action, Prefix: append([]string(nil), rule.Prefix...)})
	}
	return out
}

// DeleteApprovalRule removes the rule at index. It reports whether the index
// existed; the file is rewritten atomically.
func (m *Manager) DeleteApprovalRule(index int) (bool, error) {
	m.rulesMu.Lock()
	defer m.rulesMu.Unlock()
	rules := m.readApprovalRulesLocked()
	if index < 0 || index >= len(rules.Allow) {
		return false, nil
	}
	rules.Allow = append(rules.Allow[:index], rules.Allow[index+1:]...)
	return true, m.writeApprovalRulesLocked(rules)
}

func (m *Manager) writeApprovalRulesLocked(rules approvalRulesFile) error {
	raw, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.approvalRulesPath()), ".approval-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, m.approvalRulesPath())
}

func ruleMatches(rules approvalRulesFile, action string, fields []string) bool {
	for _, rule := range rules.Allow {
		// A single-word rule ("make", "git") is too broad to be safe; older
		// rule files may still contain them.
		if !strings.EqualFold(rule.Action, action) || len(rule.Prefix) < 2 || len(rule.Prefix) > len(fields) {
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

// unsafeRuleCommands execute arbitrary code from their arguments (or rewrite
// history), so one manual "always" must not turn into a permanent allow.
var unsafeRuleCommands = map[string]bool{
	"python": true, "python3": true, "node": true, "nodejs": true, "deno": true, "bun": true,
	"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
	"env": true, "xargs": true, "command": true, "exec": true, "eval": true,
	"nohup": true, "timeout": true, "watch": true, "setsid": true, "nice": true,
	"ionice": true, "stdbuf": true, "chroot": true, "su": true, "doas": true, "time": true,
	"ruby": true, "perl": true, "php": true, "osascript": true,
}

// optionBeforeAction reports whether a subcommand-style tool has a global
// option first (git -C repo ..., npm --prefix ...). A three-token prefix would
// then stop before the subcommand and auto-allow unrelated actions later.
func optionBeforeAction(name string, fields []string) bool {
	if len(fields) < 2 {
		return false
	}
	switch name {
	case "git", "go", "npm", "pnpm", "yarn", "cargo":
		return strings.HasPrefix(fields[1], "-")
	}
	return false
}

// ruleAllowed reports whether a remembered prefix may still auto-approve this
// invocation. Old or hand-written rules must not outlive a hardening change.
func ruleAllowed(name string, fields []string) bool {
	if unsafeRuleCommands[name] || strings.HasPrefix(name, "python") || strings.HasPrefix(name, "node") {
		return false
	}
	if optionBeforeAction(name, fields) {
		return false
	}
	if name == "git" {
		if _, sub := gitSubcommand(fields); sub != "" {
			switch sub {
			case "checkout", "reset", "clean", "push", "switch", "restore", "rebase":
				return false
			}
		}
	}
	if name == "go" && len(fields) > 1 && strings.EqualFold(fields[1], "run") {
		return false
	}
	return true
}

func (m *Manager) recordApprovalRule(action string, resources []string) {
	if !strings.EqualFold(action, "bash") && !strings.EqualFold(action, "shell") {
		return
	}
	commands, ok := splitShell(strings.Join(resources, " "))
	if !ok || len(commands) != 1 || len(commands[0]) == 0 {
		return
	}
	fields := commands[0]
	if len(fields) < 2 {
		return
	}
	name := strings.ToLower(fields[0])
	if strings.Contains(name, "/") {
		return
	}
	if !ruleAllowed(name, fields) {
		return
	}
	// Never persist a prefix for a command the deterministic layer itself
	// considers high risk.
	if decision, _ := m.argvDecision(m.cfg.Root, fields); decision == riskPrompt {
		return
	}
	prefix := fields
	if len(prefix) > 3 {
		prefix = prefix[:3]
	}
	m.rulesMu.Lock()
	defer m.rulesMu.Unlock()
	rules := m.readApprovalRulesLocked()
	for _, rule := range rules.Allow {
		if strings.EqualFold(rule.Action, action) && strings.Join(rule.Prefix, "\x00") == strings.Join(prefix, "\x00") {
			return
		}
	}
	rules.Allow = append(rules.Allow, ApprovalRule{Action: strings.ToLower(action), Prefix: prefix})
	_ = m.writeApprovalRulesLocked(rules)
}
