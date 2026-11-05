// Package verify holds the small, dependency-free validator vocabulary shared by
// objectives and plans. It must not import engine or store, so both can use it.
package verify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Spec struct {
	Type    string `json:"type"`              // exact | contains | nonempty | command
	Expect  string `json:"expect,omitempty"`  // used by exact and contains
	Command string `json:"command,omitempty"` // used by command
}

// AllowedCommands documents the accepted command shapes. Matching is by parsed
// tokens, not by string prefix: "npm run testevil" must not pass because it
// starts with "npm run test".
var AllowedCommands = []string{"node <script.js>", "npm test", "npm run test", "python3 -m unittest [args]", "python3 -m pytest [args]", "go test [pkgs]"}

func (s Spec) Validate() error {
	switch s.Type {
	case "exact", "contains":
		if strings.TrimSpace(s.Expect) == "" {
			return fmt.Errorf("validator %q needs a non-empty expect", s.Type)
		}
	case "nonempty":
	case "command":
		return CheckCommand(s.Command)
	default:
		return fmt.Errorf("unknown validator %q", s.Type)
	}
	return nil
}

// CheckCommand enforces the allow-list and rejects shell syntax.
func CheckCommand(cmd string) error {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return fmt.Errorf("command validator needs a command")
	}
	for _, bad := range []string{";", "|", "&", ">", "<", "`", "$(", "\n"} {
		if strings.Contains(cmd, bad) {
			return fmt.Errorf("command contains forbidden shell syntax %q", bad)
		}
	}
	fields := strings.Fields(cmd)
	switch fields[0] {
	case "npm":
		if cmd == "npm test" || cmd == "npm run test" {
			return nil
		}
	case "python3":
		if len(fields) >= 3 && fields[1] == "-m" && (fields[2] == "unittest" || fields[2] == "pytest") {
			return checkArgs(fields[3:])
		}
	case "go":
		if len(fields) >= 2 && fields[1] == "test" {
			return checkArgs(fields[2:])
		}
	case "node":
		if len(fields) == 2 && isScriptPath(fields[1]) {
			return nil
		}
	}
	return fmt.Errorf("command %q is not in the allow-list", cmd)
}

// checkArgs accepts only plain arguments (no flags), which keeps `-exec`,
// `-c`, `-e` and friends out of the allow-listed commands.
func checkArgs(args []string) error {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return fmt.Errorf("command flag %q is not allowed", arg)
		}
		if !isPlainArg(arg) {
			return fmt.Errorf("command argument %q is not allowed", arg)
		}
	}
	return nil
}

func isPlainArg(arg string) bool {
	if arg == "" {
		return false
	}
	// "./..." is Go's package pattern, not a parent-directory escape.
	if arg == "./..." || arg == "..." {
		return true
	}
	if strings.Contains(arg, "..") {
		return false
	}
	for _, r := range arg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '/' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

func isScriptPath(arg string) bool {
	if !isPlainArg(arg) {
		return false
	}
	for _, suffix := range []string{".js", ".mjs", ".cjs"} {
		if strings.HasSuffix(arg, suffix) {
			return true
		}
	}
	return false
}

func (s Spec) Check(result string) error {
	switch s.Type {
	case "exact":
		if result != s.Expect {
			return fmt.Errorf("artifact %q != %q", result, s.Expect)
		}
	case "contains":
		if !strings.Contains(result, s.Expect) {
			return fmt.Errorf("artifact does not contain %q", s.Expect)
		}
	case "nonempty":
		if strings.TrimSpace(result) == "" {
			return fmt.Errorf("artifact is empty")
		}
	case "command":
		return fmt.Errorf("command validators run in the engine, not here")
	default:
		return fmt.Errorf("unknown validator %q", s.Type)
	}
	return nil
}

// RunCommand executes an allow-listed command in dir without a shell. The
// Kernel uses it for per-task acceptance; delivery uses the same code for the
// assembled artifact set, so both apply identical rules.
func RunCommand(dir string, spec Spec) error {
	if err := CheckCommand(spec.Command); err != nil {
		return err
	}
	fields := strings.Fields(spec.Command)
	bin, err := lookPath(fields[0])
	if err != nil {
		return fmt.Errorf("command %q: %w", spec.Command, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, fields[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+CommandPath())
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := string(out)
		if len(msg) > 800 {
			msg = msg[len(msg)-800:]
		}
		return fmt.Errorf("command %q failed: %v: %s", spec.Command, err, strings.TrimSpace(msg))
	}
	return nil
}

// CommandPath keeps the caller's PATH but adds the usual local bin dirs; the
// daemon may run with a deliberately narrow PATH.
func CommandPath() string {
	extra := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	seen := map[string]bool{}
	var parts []string
	for _, p := range append(strings.Split(os.Getenv("PATH"), ":"), extra...) {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		parts = append(parts, p)
	}
	return strings.Join(parts, ":")
}

func lookPath(name string) (string, error) {
	if strings.ContainsRune(name, '/') {
		return name, nil
	}
	for _, dir := range strings.Split(CommandPath(), ":") {
		candidate := dir + "/" + name
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH", name)
}
