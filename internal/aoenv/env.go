// Package aoenv boots an isolated Agent Orchestrator daemon around a throwaway
// git repository. It is shared by the CLI entry points.
package aoenv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"projectos/kernel/internal/exec/ao"
)

type Env struct {
	Dir     string
	Repo    string
	BaseURL string
	DataDir string
	Port    int

	cmd     *exec.Cmd
	logFile *os.File
	owned   bool
}

// Setup prepares the sandbox repository (once) and starts the daemon.
func Setup(dir string) (*Env, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("state dir must not be empty")
	}
	env := &Env{Dir: dir, Repo: filepath.Join(dir, "repo"), DataDir: filepath.Join(dir, "data")}
	if _, err := os.Stat(filepath.Join(env.Repo, ".git")); err != nil {
		if err := env.initRepo(); err != nil {
			return nil, err
		}
	}
	// A previous scheduler may have died mid-run and left detached chat hosts.
	// Only one scheduler may own a state dir (store lock), so anything still
	// running here belongs to a dead run.
	_, _ = ao.ReapAll(env.DataDir)
	if port, ok := runningDaemon(dir); ok {
		// A previous CLI crashed but its daemon survived; reuse it instead of
		// fighting over the same data dir.
		env.Port = port
		env.BaseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
		env.owned = false
		if err := waitReady(env.BaseURL, 10*time.Second); err == nil {
			return env, nil
		}
	}
	env.Port = freePort()
	env.BaseURL = fmt.Sprintf("http://127.0.0.1:%d", env.Port)
	if err := env.startDaemon(); err != nil {
		return nil, err
	}
	env.owned = true
	if err := waitReady(env.BaseURL, 40*time.Second); err != nil {
		env.Close()
		return nil, err
	}
	return env, nil
}

// initRepo builds the throwaway git repository with a resolvable default branch.
func (e *Env) initRepo() error {
	origin := filepath.Join(e.Dir, "origin.git")
	if err := os.MkdirAll(e.Repo, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(origin); err != nil {
		if err := gitRun("git", "init", "-q", "--bare", origin); err != nil {
			return err
		}
	}
	if err := gitRun("git", "-C", e.Repo, "init", "-q", "-b", "main"); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(e.Repo, "README.md"), []byte("# kernel sandbox\n"), 0o644); err != nil {
		return err
	}
	if err := gitRun("git", "-C", e.Repo, "-c", "user.email=k@local", "-c", "user.name=k", "add", "."); err != nil {
		return err
	}
	if err := gitRun("git", "-C", e.Repo, "-c", "user.email=k@local", "-c", "user.name=k", "commit", "-q", "-m", "init"); err != nil {
		return err
	}
	if err := gitRun("git", "-C", e.Repo, "remote", "add", "origin", origin); err != nil {
		return err
	}
	if err := gitRun("git", "-C", e.Repo, "push", "-q", "-u", "origin", "main"); err != nil {
		return err
	}
	return gitRun("git", "-C", e.Repo, "remote", "set-head", "origin", "main")
}

func (e *Env) startDaemon() error {
	bin := os.Getenv("AO_BIN")
	if bin == "" {
		if u, err := user.Current(); err == nil {
			bin = filepath.Join(u.HomeDir, "dev", "projectos-spike", ".scratch", "p1", "bin", "ao")
		}
	}
	cmd := exec.Command(bin, "daemon")
	cmd.Dir = e.Dir
	cmd.Env = append(os.Environ(),
		"AO_DATA_DIR="+e.DataDir,
		"AO_RUN_FILE="+filepath.Join(e.Dir, "running.json"),
		fmt.Sprintf("AO_PORT=%d", e.Port),
		"AO_SHUTDOWN_TIMEOUT=5s",
		"AO_TELEMETRY_EVENTS=off", "AO_TELEMETRY_METRICS=off", "AO_TELEMETRY_REMOTE=off",
		"AO_HARNESS_UPDATE_CHECKS=off", "AO_UPDATE_WHILE_RUNNING=off",
		"AO_VERSION_REFRESH=off", "AO_TUI_VERSION_REFRESH=off",
	)
	log, err := os.Create(filepath.Join(e.Dir, "daemon.log"))
	if err != nil {
		return err
	}
	e.logFile = log
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		return err
	}
	e.cmd = cmd
	return nil
}

func (e *Env) Close() {
	if !e.owned {
		if e.logFile != nil {
			_ = e.logFile.Close()
		}
		return
	}
	if e.cmd != nil && e.cmd.Process != nil && e.cmd.ProcessState == nil {
		_ = e.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _, _ = e.cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = e.cmd.Process.Kill()
		}
	}
	_, _ = ao.ReapAll(e.DataDir)
	if e.logFile != nil {
		_ = e.logFile.Close()
	}
}

func (e *Env) CreateProject() (string, error) {
	body, _ := json.Marshal(map[string]string{"path": e.Repo})
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(e.BaseURL+"/api/v1/projects", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusConflict {
		return e.findProject()
	}
	if resp.StatusCode != 201 {
		return "", fmt.Errorf("create project: %d %s", resp.StatusCode, raw)
	}
	var out struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out.Project.ID, nil
}

// runningDaemon reports a live daemon recorded in running.json.
func runningDaemon(dir string) (int, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "running.json"))
	if err != nil {
		return 0, false
	}
	var state struct {
		PID  int `json:"pid"`
		Port int `json:"port"`
	}
	if err := json.Unmarshal(raw, &state); err != nil || state.PID <= 0 || state.Port <= 0 {
		return 0, false
	}
	proc, err := os.FindProcess(state.PID)
	if err != nil {
		return 0, false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return 0, false
	}
	return state.Port, true
}

func (e *Env) findProject() (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(e.BaseURL + "/api/v1/projects")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Projects []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"projects"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	for _, p := range out.Projects {
		if p.Path == e.Repo {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("project for %s not found after conflict", e.Repo)
}

func gitRun(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitReady(base string, timeout time.Duration) error {
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("daemon not ready within %s", timeout)
}
