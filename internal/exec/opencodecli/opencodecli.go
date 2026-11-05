// Package opencodecli runs tasks through the OpenCode command-line client
// directly, without the AO daemon. Each task gets its own directory; the model
// is passed straight through, so any provider registered in the harness works.
package opencodecli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/exec"
)

type Config struct {
	Model    string // provider/model
	Bin      string // defaults to "opencode"
	BaseDir  string // seeded into every task dir (usually <state>/repo)
	WorkRoot string // usually <state>/work
}

type Backend struct {
	cfg Config

	mu   sync.Mutex
	runs map[string]*run
}

type run struct {
	id          string
	dir         string
	logPath     string
	promptChars int
	seed        map[string]string
	cmd         *osexec.Cmd
	done        chan struct{}
	err         error
	usage       usage
}

type usage struct {
	input   int
	output  int
	session string
}

func New(cfg Config) *Backend {
	if cfg.Bin == "" {
		cfg.Bin = "opencode"
	}
	b := &Backend{cfg: cfg, runs: map[string]*run{}}
	// A previous Kernel may have died mid-run; those opencode processes are
	// orphans and must not keep writing into a workdir nobody watches.
	b.reapStale()
	return b
}

func (b *Backend) Name() string { return "opencode" }

func (b *Backend) Start(ctx context.Context, req exec.StartRequest) (exec.StartResult, error) {
	if strings.TrimSpace(b.cfg.Model) == "" {
		return exec.StartResult{}, fmt.Errorf("opencode: model is required")
	}
	id := fmt.Sprintf("%s-%d-%d", req.TaskID, req.Attempt, time.Now().UnixNano())
	dir := filepath.Join(b.cfg.WorkRoot, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return exec.StartResult{}, err
	}
	seed, err := b.materialize(dir)
	if err != nil {
		return exec.StartResult{}, err
	}
	logPath := filepath.Join(dir, ".kernel-run.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return exec.StartResult{}, err
	}

	args := []string{"run", "--standalone", "--format", "json", "-m", b.cfg.Model}
	// Only unattended workers get auto-approval. Reviewers, planners and the
	// reporter declare ApprovalMode "default" and must not be able to write.
	if req.ApprovalMode != "default" {
		args = append(args, "--auto")
	}
	// Retrying the same task should continue the provider session; a fresh
	// session loses every finding the worker already reasoned about.
	if strings.TrimSpace(req.SessionID) != "" {
		args = append(args, "--session", req.SessionID)
	}
	args = append(args, req.Prompt)
	cmd := osexec.Command(b.cfg.Bin, args...)
	cmd.Dir = dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Its own process group so Cancel can stop the whole tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return exec.StartResult{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, ".kernel-run.pid"), []byte(fmt.Sprint(cmd.Process.Pid)), 0o644); err != nil {
		_ = cmd.Process.Kill()
		logFile.Close()
		return exec.StartResult{}, err
	}

	r := &run{id: id, dir: dir, logPath: logPath, promptChars: len(req.Prompt), seed: seed,
		cmd: cmd, done: make(chan struct{})}
	go func() {
		r.err = cmd.Wait()
		logFile.Close()
		_ = os.Remove(filepath.Join(dir, ".kernel-run.pid"))
		close(r.done)
	}()

	b.mu.Lock()
	b.runs[id] = r
	b.mu.Unlock()
	return exec.StartResult{ExternalID: id}, nil
}

func (b *Backend) Observe(ctx context.Context, externalID string) (exec.State, error) {
	r, err := b.get(externalID)
	if err != nil {
		return exec.State{}, err
	}
	select {
	case <-r.done:
		if r.err == nil {
			return exec.State{Status: domain.RunSucceeded}, nil
		}
		return exec.State{Status: domain.RunFailed, Evidence: tail(r.logPath, 400)}, nil
	default:
		return exec.State{Status: domain.RunRunning}, nil
	}
}

func (b *Backend) Collect(ctx context.Context, externalID, artifactPath string) (exec.Artifact, error) {
	r, err := b.get(externalID)
	if err != nil {
		return exec.Artifact{}, err
	}
	select {
	case <-r.done:
	default:
		return exec.Artifact{}, fmt.Errorf("run %s is still running", externalID)
	}
	files := changedFiles(r.dir, r.seed)
	artifact := exec.Artifact{Files: files, PromptChars: r.promptChars,
		PromptTokens: r.promptChars / 4, UsageEstimated: true}

	// Usage is independent of where the artifact comes from: always parse the
	// event stream, otherwise a worker with an artifact spends tokens we never
	// record.
	waitLogStable(r.logPath)
	text, u := parseOutput(r.logPath)
	artifact.SessionID = u.session
	if u.input > 0 || u.output > 0 {
		// The CLI reports provider tokens. Keep chars in sync for old readers,
		// but the budget and the cockpit read the token fields.
		artifact.PromptChars = u.input * 4
		artifact.OutputChars = u.output * 4
		artifact.PromptTokens = u.input
		artifact.OutputTokens = u.output
		artifact.UsageEstimated = false
	} else if artifact.Result != "" {
		artifact.OutputTokens = len(artifact.Result) / 4
	}
	if strings.TrimSpace(artifactPath) != "" {
		content, err := os.ReadFile(filepath.Join(r.dir, filepath.FromSlash(artifactPath)))
		if err != nil {
			return exec.Artifact{}, fmt.Errorf("read artifact %s: %w", artifactPath, err)
		}
		artifact.Result = string(content)
	} else {
		artifact.Result = text
	}
	artifact.Version = digest(artifact.Result)
	return artifact, nil
}

func (b *Backend) Cancel(ctx context.Context, externalID string) error {
	r, err := b.get(externalID)
	if err != nil {
		return err
	}
	select {
	case <-r.done:
		return nil
	default:
	}
	if r.cmd.Process != nil {
		if err := syscall.Kill(-r.cmd.Process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		select {
		case <-r.done:
			return nil
		case <-time.After(5 * time.Second):
		}
		if err := syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		select {
		case <-r.done:
			return nil
		case <-time.After(5 * time.Second):
			return fmt.Errorf("process %d did not exit after SIGKILL", r.cmd.Process.Pid)
		}
	}
	return nil
}

func (b *Backend) get(id string) (*run, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.runs[id]
	if !ok {
		return nil, fmt.Errorf("unknown run %s", id)
	}
	return r, nil
}

// materialize prepares the task directory: a git worktree when the base is a
// repository (cheap, isolated, diff-able), otherwise a plain copy.
func (b *Backend) materialize(dir string) (map[string]string, error) {
	if isGitRepo(b.cfg.BaseDir) {
		if seed, err := b.worktree(dir); err == nil {
			return seed, nil
		}
		// fall through to a copy if worktree creation is not possible
	}
	return seedDir(b.cfg.BaseDir, dir)
}

func isGitRepo(base string) bool {
	if strings.TrimSpace(base) == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(base, ".git"))
	return err == nil
}

func (b *Backend) worktree(dir string) (map[string]string, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, err
	}
	cmd := osexec.Command("git", "-C", b.cfg.BaseDir, "worktree", "add", "--detach", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("worktree: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return snapshotWorktree(dir)
}

func snapshotWorktree(dir string) (map[string]string, error) {
	seed := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		seed[filepath.ToSlash(rel)] = digest(string(content))
		return nil
	})
	return seed, err
}

// reapStale kills process groups left behind by a previous Kernel that died.
func (b *Backend) reapStale() {
	entries, err := os.ReadDir(b.cfg.WorkRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		dir := filepath.Join(b.cfg.WorkRoot, entry.Name())
		raw, err := os.ReadFile(filepath.Join(dir, ".kernel-run.pid"))
		if err != nil {
			continue
		}
		var pid int
		if _, err := fmt.Sscan(strings.TrimSpace(string(raw)), &pid); err != nil || pid <= 0 {
			continue
		}
		if processMatches(pid, dir) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		_ = os.Remove(filepath.Join(dir, ".kernel-run.pid"))
	}
}

// processMatches checks that the live pid is still our opencode run for this
// workdir, so PID reuse does not kill an unrelated process.
// samePath compares paths after resolving symlinks, so /var/... and
// /private/var/... are recognised as the same directory.
func samePath(a, b string) bool {
	ra, erra := filepath.EvalSymlinks(a)
	rb, errb := filepath.EvalSymlinks(b)
	if erra == nil && errb == nil {
		return ra == rb
	}
	return a == b
}

func processMatches(pid int, dir string) bool {
	// The child's cwd is the strongest signal: opencode is started with dir set,
	// and its argv does not necessarily contain the path. Fall back to the
	// command line when lsof is unavailable.
	if out, err := osexec.Command("lsof", "-a", "-p", fmt.Sprint(pid), "-d", "cwd", "-Fn").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "n") {
				continue
			}
			if samePath(strings.TrimPrefix(line, "n"), dir) {
				return true
			}
		}
		return false
	}
	out, err := osexec.Command("ps", "-p", fmt.Sprint(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), dir)
}

// seedDir copies the base repository into the task directory (without .git) and
// returns a hash snapshot so Collect can report only changed files.
func seedDir(base, dst string) (map[string]string, error) {
	seed := map[string]string{}
	if strings.TrimSpace(base) == "" {
		return seed, nil
	}
	if _, err := os.Stat(base); err != nil {
		return seed, nil
	}
	err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(base, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if werr := os.WriteFile(filepath.Join(dst, rel), content, 0o644); werr != nil {
			return werr
		}
		seed[filepath.ToSlash(rel)] = digest(string(content))
		return nil
	})
	return seed, err
}

func changedFiles(dir string, seed map[string]string) []string {
	var files []string
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == ".kernel-run.log" || rel == ".kernel-run.pid" || strings.HasPrefix(rel, ".git/") {
			return nil
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if old, ok := seed[rel]; !ok || old != digest(string(content)) {
			files = append(files, rel)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// parseOutput reads the JSONL event stream and returns the assistant text plus
// token usage from the last step.
func parseOutput(path string) (string, usage) {
	file, err := os.Open(path)
	if err != nil {
		return "", usage{}
	}
	defer file.Close()
	var text strings.Builder
	var u usage
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionID"`
			Tokens    struct {
				Input  int `json:"input"`
				Output int `json:"output"`
			} `json:"tokens"`
			Part struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Tokens struct {
					Input  int `json:"input"`
					Output int `json:"output"`
				} `json:"tokens"`
			} `json:"part"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if u.session == "" && event.SessionID != "" {
			u.session = event.SessionID
		}
		if event.Type == "text" && event.Part.Type == "text" {
			text.WriteString(event.Part.Text)
		}
		if event.Type == "step_finish" {
			// The real CLI nests usage under part.tokens; older fixtures put it
			// at the top level. Accept both so the parser does not silently
			// report every completed run as "estimated".
			input, output := event.Part.Tokens.Input, event.Part.Tokens.Output
			if input == 0 && output == 0 {
				input, output = event.Tokens.Input, event.Tokens.Output
			}
			if input > 0 || output > 0 {
				u.input += input
				u.output += output
			}
		}
	}
	if text.Len() == 0 {
		return tail(path, 2000), u
	}
	return strings.TrimSpace(text.String()), u
}

// waitLogStable waits until the JSONL file stops growing. `opencode run` may
// exit before a child process finishes writing the final step_finish, so parsing
// immediately records real provider usage as an estimate (and undercounts the
// budget). A stable file for ~300ms is enough in practice.
func waitLogStable(path string) {
	last := int64(-1)
	stable := 0
	for i := 0; i < 40; i++ {
		info, err := os.Stat(path)
		if err != nil {
			return
		}
		if info.Size() == last {
			stable++
		} else {
			stable = 0
			last = info.Size()
		}
		if stable >= 3 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func tail(path string, n int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(raw))
	if len(text) <= n {
		return text
	}
	return text[len(text)-n:]
}
