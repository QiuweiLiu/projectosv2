package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
)

// controlServer relays writes to `kernel goal ...` subprocesses. It never opens
// the database for writing: the Kernel stays the only writer, which is what
// makes the single-writer and recovery guarantees hold.
type controlServer struct {
	binary string
	ws     *workspace
	token  string
	mu     sync.Mutex
	jobs   map[string]string // objective id -> last known status
	busy   map[string]bool   // objective id -> a plan/run is already in flight
}

func newControl(binary string, ws *workspace) *controlServer {
	return &controlServer{binary: binary, ws: ws, token: loadToken(ws.root),
		jobs: map[string]string{}, busy: map[string]bool{}}
}

// loadToken keeps the token stable across restarts so an open dashboard does not
// start failing after `serve --stop && serve --daemon`.
func loadToken(dir string) string {
	if raw, err := os.ReadFile(controlTokenFile(dir)); err == nil {
		if token := strings.TrimSpace(string(raw)); token != "" {
			return token
		}
	}
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func (c *controlServer) authorised(r *http.Request) bool {
	if r.Header.Get("X-Kernel-Token") != c.token {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return false
	}
	switch parsed.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

type goalRequest struct {
	Goal        string `json:"goal"`
	Title       string `json:"title"`
	ProjectID   string `json:"projectId"`
	Constraints string `json:"constraints,omitempty"`
	Expected    string `json:"expected,omitempty"`
	Acceptance  string `json:"acceptance,omitempty"`
	RunNow      bool   `json:"runNow"`
	Reviewer    bool   `json:"reviewer"`
}

func (c *controlServer) createGoal(w http.ResponseWriter, r *http.Request) {
	if !c.authorised(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req goalRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	req.Goal = strings.TrimSpace(req.Goal)
	if req.Goal == "" {
		http.Error(w, "goal is required", http.StatusBadRequest)
		return
	}
	id, err := c.createObjective(req)
	if err != nil {
		http.Error(w, "create: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = writeJSON(w, map[string]string{"objective": id, "status": "draft"})
	if req.RunNow {
		// Legacy `runNow` used to mean plan + confirm + run in one request. That
		// skipped the exact confirmation the workflow exists to enforce. It now
		// only plans; the caller must confirm and run explicitly.
		go c.planOnly(id)
	}
}

// createObjective is shared by the HTTP create endpoint and the chat handoff
// confirmation. It never confirms a plan or starts a run.
func (c *controlServer) createObjective(req goalRequest) (string, error) {
	if strings.TrimSpace(req.Goal) == "" {
		return "", fmt.Errorf("goal is required")
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		projectID = defaultProjectID
	}
	if _, ok, err := c.ws.FindProject(projectID); err != nil || !ok {
		return "", fmt.Errorf("unknown project")
	}
	dir, err := c.ws.newProjectDir()
	if err != nil {
		return "", err
	}
	if err := writeProjectMeta(dir, projectID); err != nil {
		return "", err
	}
	args := []string{"goal", "create", "--dir", dir, "--goal", req.Goal}
	if req.Title != "" {
		args = append(args, "--title", req.Title)
	}
	if req.Constraints != "" {
		args = append(args, "--constraints", req.Constraints)
	}
	if req.Expected != "" {
		args = append(args, "--expected", req.Expected)
	}
	if req.Acceptance != "" {
		args = append(args, "--acceptance", req.Acceptance)
	}
	if _, err := c.runGoal(dir, args...); err != nil {
		return "", err
	}
	id, err := objectiveInDir(dir)
	if err != nil {
		return "", err
	}
	dir = c.ws.adopt(dir, id)
	c.set(id, "draft")
	c.ws.invalidate()
	_, _ = c.ws.ProjectSnapshot(projectID)
	return id, nil
}

// objectiveInDir reads the id back from the database instead of scraping CLI
// output, so the create path never depends on stdout formatting.
func objectiveInDir(dir string) (string, error) {
	st, err := store.OpenReadOnly(filepath.Join(dir, "kernel.db"))
	if err != nil {
		return "", err
	}
	defer st.Close()
	objectives, err := st.ListObjectives()
	if err != nil {
		return "", err
	}
	if len(objectives) == 0 {
		return "", fmt.Errorf("no objective was created")
	}
	return objectives[0].ID, nil
}

func (c *controlServer) planOnly(id string) {
	if !c.claim(id) {
		c.set(id, "another action is already running")
		return
	}
	defer c.release(id)
	p, ok, err := c.ws.find(id)
	if err != nil || !ok {
		c.set(id, "objective not found")
		return
	}
	c.set(id, "planning")
	args := []string{"goal", "plan", "--dir", p.Dir, "--planner", "model"}
	if ctx := c.ws.ProjectContextText(p.ProjectID); ctx != "" {
		args = append(args, "--context", ctx)
	}
	if _, err := c.runGoal(p.Dir, args...); err != nil {
		c.set(id, "plan failed: "+err.Error())
		return
	}
	c.set(id, "planned")
}

type runRequest struct {
	Reviewer    bool   `json:"reviewer"`
	ApproveRisk bool   `json:"approveRisk"`
	MaxRuns     int    `json:"maxRuns"`
	MaxReviews  int    `json:"maxReviews"`
	MaxTokens   int    `json:"maxTokens"`
	RunTimeout  string `json:"runTimeout"`
}

func (c *controlServer) runArgs(dir string, req runRequest) []string {
	maxRuns, maxReviews, maxTokens := req.MaxRuns, req.MaxReviews, req.MaxTokens
	if maxRuns <= 0 {
		maxRuns = 8
	}
	if maxReviews <= 0 {
		maxReviews = 8
	}
	if maxTokens <= 0 {
		maxTokens = 600000
	}
	args := []string{"goal", "run", "--dir", dir, "--executor", "opencode",
		"--max-runs", fmt.Sprint(maxRuns), "--max-reviews", fmt.Sprint(maxReviews),
		"--max-tokens", fmt.Sprint(maxTokens)}
	if req.Reviewer {
		args = append(args, "--reviewer")
	}
	if req.ApproveRisk {
		args = append(args, "--approve-risk")
	}
	if strings.TrimSpace(req.RunTimeout) != "" {
		args = append(args, "--run-timeout", req.RunTimeout)
	}
	return args
}

// objectiveAction handles POST /api/objectives/{id}/{confirm|run|cancel}.
func (c *controlServer) objectiveAction(w http.ResponseWriter, r *http.Request, id, action string) {
	if !c.authorised(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	p, ok, err := c.ws.find(id)
	if err != nil || !ok {
		http.Error(w, "unknown objective", http.StatusNotFound)
		return
	}
	switch action {
	case "plan":
		if !c.claim(id) {
			http.Error(w, "an action is already running for this objective", http.StatusConflict)
			return
		}
		// Planning is a proposal, not a side effect: it is safe to run without a
		// second confirmation, but the plan still needs an explicit confirm.
		go func() {
			defer c.release(id)
			c.set(id, "planning")
			args := []string{"goal", "plan", "--dir", p.Dir, "--planner", "model"}
			if ctx := c.ws.ProjectContextText(p.ProjectID); ctx != "" {
				args = append(args, "--context", ctx)
			}
			if _, err := c.runGoal(p.Dir, args...); err != nil {
				c.set(id, "plan failed: "+err.Error())
				return
			}
			c.set(id, "planned")
		}()
		writeJSON(w, map[string]string{"objective": id, "status": "planning"})
	case "confirm":
		if _, err := c.runGoal(p.Dir, "goal", "confirm", "--dir", p.Dir); err != nil {
			http.Error(w, "confirm: "+err.Error(), http.StatusConflict)
			return
		}
		c.set(id, "confirmed")
		writeJSON(w, map[string]string{"objective": id, "status": "confirmed"})
	case "run":
		var req runRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil && err != io.EOF {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if !c.claim(id) {
			http.Error(w, "an action is already running for this objective", http.StatusConflict)
			return
		}
		go func() {
			defer c.release(id)
			c.set(id, "running")
			_, err := c.runGoal(p.Dir, c.runArgs(p.Dir, req)...)
			c.settleRun(id, err)
		}()
		writeJSON(w, map[string]string{"objective": id, "status": "running"})
	case "continue":
		var req struct {
			Goal string `json:"goal"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Goal) == "" {
			http.Error(w, "goal is required", http.StatusBadRequest)
			return
		}
		if !c.claim(id) {
			http.Error(w, "an action is already running for this objective", http.StatusConflict)
			return
		}
		go func() {
			defer c.release(id)
			c.set(id, "planning continuation")
			args := []string{"goal", "continue", "--dir", p.Dir, "--goal", req.Goal}
			if ctx := c.ws.ProjectContextText(p.ProjectID); ctx != "" {
				args = append(args, "--context", ctx)
			}
			if _, err := c.runGoal(p.Dir, args...); err != nil {
				c.set(id, "continue failed: "+err.Error())
				return
			}
			c.set(id, "continuation planned")
		}()
		writeJSON(w, map[string]string{"objective": id, "status": "planning continuation"})
	case "report":
		if !c.claim(id) {
			http.Error(w, "an action is already running for this objective", http.StatusConflict)
			return
		}
		go func() {
			defer c.release(id)
			c.set(id, "writing report")
			if _, err := c.runGoal(p.Dir, "goal", "report", "--dir", p.Dir, "--llm"); err != nil {
				c.set(id, "report failed: "+err.Error())
				return
			}
			c.set(id, "report ready")
		}()
		writeJSON(w, map[string]string{"objective": id, "status": "reporting"})
	case "pause":
		if _, err := c.runGoal(p.Dir, "goal", "pause", "--dir", p.Dir); err != nil {
			http.Error(w, "pause: "+err.Error(), http.StatusConflict)
			return
		}
		c.set(id, "paused")
		_ = writeJSON(w, map[string]string{"objective": id, "status": "pause requested"})
	case "unpause":
		if _, err := c.runGoal(p.Dir, "goal", "unpause", "--dir", p.Dir); err != nil {
			http.Error(w, "unpause: "+err.Error(), http.StatusConflict)
			return
		}
		c.set(id, "running")
		_ = writeJSON(w, map[string]string{"objective": id, "status": "resumed"})
	case "cancel":
		reason := "cancelled from the dashboard"
		var req struct {
			Reason string `json:"reason"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil && err != io.EOF {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Reason) != "" {
			reason = req.Reason
		}
		// Cancelling is cooperative: the flag file is consumed by whichever
		// scheduler owns the state dir, and that scheduler stays the only writer.
		go func() {
			c.set(id, "cancelling")
			if _, err := c.runGoal(p.Dir, "goal", "cancel", "--dir", p.Dir, "--reason", reason); err != nil {
				c.set(id, "cancel failed: "+err.Error())
				return
			}
			c.set(id, "cancelled")
		}()
		writeJSON(w, map[string]string{"objective": id, "status": "cancelling"})
	default:
		http.Error(w, "unknown action", http.StatusNotFound)
	}
}

// settleRun reports the user's intent when a run stops: a cancelled objective
// exits non-zero, and calling that a failure hides what actually happened.
func (c *controlServer) settleRun(id string, runErr error) {
	// Keep the project-level derived snapshot current after a managed run.
	defer func() {
		if p, ok, _ := c.ws.find(id); ok {
			_, _ = c.ws.ProjectSnapshot(p.ProjectID)
			c.ws.invalidate()
		}
	}()
	if runErr == nil {
		c.set(id, "completed")
		return
	}
	if p, ok, _ := c.ws.find(id); ok {
		if st, err := store.OpenReadOnly(filepath.Join(p.Dir, "kernel.db")); err == nil {
			obj, found, _ := st.GetObjective(id)
			st.Close()
			if found && obj.Status == goal.StatusCancelled {
				c.set(id, "cancelled")
				return
			}
		}
	}
	c.set(id, "run failed: "+runErr.Error())
}

// explainTask asks the reporter to translate one task's findings. Cancel and
// other objective actions are independent of this.
func (c *controlServer) explainTask(w http.ResponseWriter, r *http.Request, id string) {
	if !c.authorised(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	p, ok, err := c.ws.resolve(r.URL.Query().Get("objective"))
	if err != nil || !ok {
		http.Error(w, "objective is required", http.StatusBadRequest)
		return
	}
	st, err := store.OpenReadOnly(filepath.Join(p.Dir, "kernel.db"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, found, err := st.Get(domain.TaskID(id))
	st.Close()
	if err != nil || !found {
		http.Error(w, "unknown task", http.StatusNotFound)
		return
	}
	key := "explain:" + id
	if !c.claim(key) {
		http.Error(w, "an explanation is already being written", http.StatusConflict)
		return
	}
	go func() {
		defer c.release(key)
		c.set(id, "explaining findings")
		if _, err := c.runGoal(p.Dir, "goal", "explain", "--dir", p.Dir, "--task", id); err != nil {
			c.set(id, "explain failed: "+err.Error())
			return
		}
		c.set(id, "explained")
	}()
	writeJSON(w, map[string]string{"task": id, "status": "explaining"})
}

// updateModels writes one role's model or refreshes the cached list. Writes go
// through the CLI so the state dir keeps a single writer.
func (c *controlServer) updateModels(w http.ResponseWriter, r *http.Request) {
	if !c.authorised(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Role    string `json:"role"`
		Model   string `json:"model"`
		Refresh bool   `json:"refresh"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Role) == "" && !req.Refresh {
		http.Error(w, "role or refresh is required", http.StatusBadRequest)
		return
	}
	args := []string{"goal", "models", "--dir", c.ws.root}
	if req.Refresh {
		args = append(args, "--refresh")
	}
	if req.Role != "" {
		args = append(args, "--set", req.Role+"="+req.Model)
	}
	if _, err := c.runGoal(c.ws.root, args...); err != nil {
		http.Error(w, "models: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// runGoal runs a child in its own session with output going to a file, not a
// pipe. A pipe would SIGPIPE the worker if this server died mid-run, which is
// exactly the failure `serve --stop` must not introduce.
func (c *controlServer) runGoal(dir string, args ...string) (string, error) {
	logPath := filepath.Join(c.ws.root, "goals.log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", err
	}
	defer file.Close()
	fmt.Fprintf(file, "\n=== %s kernel %s\n", time.Now().Format(time.RFC3339), strings.Join(args, " "))
	cmd := exec.Command(c.binary, args...)
	cmd.Env = append(os.Environ(),
		"KERNEL_MODELS_FILE="+filepath.Join(c.ws.root, "models.json"),
		"KERNEL_MODELS_CACHE="+filepath.Join(c.ws.root, "models-cache.json"))
	cmd.Stdout = file
	cmd.Stderr = file
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Run(); err != nil {
		// Surface the child's own error text: "exit status 1" alone tells the
		// operator nothing about why planning or running stopped.
		tail := tailFile(logPath, 400)
		if tail != "" {
			return tail, fmt.Errorf("%v: %s", err, tail)
		}
		return "", err
	}
	return "", nil
}

func tailFile(path string, n int) string {
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

// claim makes plan/run single-flight per objective. Cancel is deliberately not
// gated: it must be possible to stop a run while one is in progress.
func (c *controlServer) claim(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.busy[id] {
		return false
	}
	c.busy[id] = true
	return true
}

func (c *controlServer) release(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.busy, id)
}

func (c *controlServer) set(id, status string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.jobs[id] = status
}

func (c *controlServer) jobStatus(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = writeJSON(w, c.jobs)
}

func controlTokenFile(dir string) string { return filepath.Join(dir, "control-token") }

func writeToken(dir, token string) error {
	path := controlTokenFile(dir)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	// WriteFile only applies the mode at creation; an older 0644 file stays 0644.
	return os.Chmod(path, 0o600)
}
