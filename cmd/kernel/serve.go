package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"projectos/kernel/internal/chat"
	"projectos/kernel/internal/conversation"
	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/modelcfg"
	"projectos/kernel/internal/store"
)

var controlOn bool

// serveInstance identifies this process in /api/status. --stop refuses to signal
// a PID whose answers do not carry the instance recorded in serve.json, which is
// what makes PID reuse harmless.
var serveInstance string

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// serveState is the handshake between `serve --daemon` and every later command:
// it is how --stop, --status and the cockpit find a running server.
type serveState struct {
	PID      int    `json:"pid"`
	Addr     string `json:"addr"`
	Control  bool   `json:"control"`
	Started  string `json:"started"`
	Instance string `json:"instance,omitempty"`
}

func serveStatePath(dir string) string { return filepath.Join(dir, "serve.json") }

func readServeState(dir string) (serveState, bool) {
	raw, err := os.ReadFile(serveStatePath(dir))
	if err != nil {
		return serveState{}, false
	}
	var state serveState
	if json.Unmarshal(raw, &state) != nil || state.PID <= 0 {
		return serveState{}, false
	}
	return state, true
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// httpBase turns a listen address into a URL we can poll.
func httpBase(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr
	}
	return "http://" + addr
}

// cmdServe exposes a read-only dashboard for one state dir. With --control it
// also accepts writes, but those are relayed to `kernel goal ...` subprocesses
// so the Kernel stays the only writer.
func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	addr := fs.String("addr", "127.0.0.1:8787", "listen address")
	enableControl := fs.Bool("control", false, "enable writes (goal submit, confirm, run, cancel); token protected")
	background := fs.Bool("daemon", false, "run detached in the background")
	allowRemote := fs.Bool("allow-remote", false, "bind a non-loopback address (reads are unauthenticated)")
	stop := fs.Bool("stop", false, "stop the background server")
	show := fs.Bool("status", false, "print the background server state")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "serve requires --dir")
		return exitError
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		return exitError
	}
	if !*allowRemote && !isLoopbackAddr(*addr) {
		fmt.Fprintf(os.Stderr, "refusing to bind %q: read APIs have no auth; use 127.0.0.1 or pass --allow-remote\n", *addr)
		return exitError
	}
	switch {
	case *stop:
		return serveStop(*dir)
	case *show:
		return serveStatePrint(*dir)
	case *background:
		return serveBackground(*dir, *addr, *enableControl, *allowRemote)
	}
	return serveForeground(*dir, *addr, *enableControl)
}

// liveInstance asks the server who it is. Used by --stop and --status so a
// recycled PID is never mistaken for the process we started.
func liveInstance(addr string) (string, bool) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(httpBase(addr) + "/api/status")
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var body struct {
		Instance string `json:"instance"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false
	}
	return body.Instance, true
}

func serveStatePrint(dir string) int {
	state, ok := readServeState(dir)
	if !ok {
		fmt.Println("not running")
		return exitOK
	}
	alive := processAlive(state.PID)
	verified := false
	if alive {
		if live, ok := liveInstance(state.Addr); ok && (state.Instance == "" || live == state.Instance) {
			verified = true
		}
	}
	fmt.Printf("pid=%d addr=%s control=%v started=%s alive=%v verified=%v\n",
		state.PID, state.Addr, state.Control, state.Started, alive, verified)
	if !alive {
		return exitBlocked
	}
	return exitOK
}

func serveBackground(dir, addr string, control, allowRemote bool) int {
	instance := newInstance()
	if state, ok := readServeState(dir); ok && processAlive(state.PID) {
		fmt.Printf("already running pid=%d http://%s\n", state.PID, state.Addr)
		return exitOK
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		return exitError
	}
	logPath := filepath.Join(dir, "serve.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve log:", err)
		return exitError
	}
	childArgs := []string{"serve", "--dir", dir, "--addr", addr}
	if control {
		childArgs = append(childArgs, "--control")
	}
	if allowRemote {
		childArgs = append(childArgs, "--allow-remote")
	}
	cmd := exec.Command(self, childArgs...)
	cmd.Env = append(os.Environ(), "KERNEL_SERVE_DAEMON=1", "KERNEL_SERVE_INSTANCE="+instance)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// Setsid detaches the server from this terminal: closing the window, or the
	// shell exiting, must not take the server (and the tasks it launched) down.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		fmt.Fprintln(os.Stderr, "serve:", err)
		return exitError
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	_ = logFile.Close()

	seen, err := waitReady(addr, 15*time.Second)
	if err != nil {
		// Do not leave an undiscoverable server behind: without serve.json, --stop
		// cannot find it.
		_ = cmd.Process.Kill()
		fmt.Fprintf(os.Stderr, "server did not become ready: %v\n", err)
		if tail := logTail(logPath, 3); tail != "" {
			fmt.Fprintln(os.Stderr, tail)
		}
		return exitError
	}
	if !processAlive(pid) {
		fmt.Fprintf(os.Stderr, "server exited during startup; see %s\n", logPath)
		return exitError
	}
	if seen != instance {
		_ = cmd.Process.Kill()
		fmt.Fprintf(os.Stderr, "port %s is served by something else (instance mismatch); refusing to adopt it\n", addr)
		return exitError
	}
	state := serveState{PID: pid, Addr: addr, Control: control,
		Started: time.Now().UTC().Format(time.RFC3339), Instance: instance}
	raw, _ := json.MarshalIndent(state, "", "  ")
	if err := os.WriteFile(serveStatePath(dir), raw, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "serve state:", err)
		return exitError
	}
	fmt.Printf("started pid=%d %s (log %s)\n", pid, httpBase(addr), logPath)
	if control {
		if raw, err := os.ReadFile(controlTokenFile(dir)); err == nil {
			fmt.Printf("control token: %s\n", strings.TrimSpace(string(raw)))
		}
	}
	return exitOK
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false // includes ":8787", which binds every interface
}

func waitReady(addr string, within time.Duration) (string, error) {
	deadline := time.Now().Add(within)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	var last error
	for time.Now().Before(deadline) {
		resp, err := client.Get(httpBase(addr) + "/api/status")
		if err == nil {
			var body struct {
				Instance string `json:"instance"`
			}
			if resp.StatusCode == http.StatusOK {
				_ = json.NewDecoder(resp.Body).Decode(&body)
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return body.Instance, nil
			}
			last = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			last = err
		}
		time.Sleep(150 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("timeout")
	}
	return "", last
}

// logTail shows the child's own reason for exiting instead of just a dial error.
func logTail(path string, lines int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

func newInstance() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func serveStop(dir string) int {
	state, ok := readServeState(dir)
	if !ok {
		fmt.Println("not running (no serve.json)")
		return exitOK
	}
	if !processAlive(state.PID) {
		_ = os.Remove(serveStatePath(dir))
		fmt.Printf("stale pid %d removed\n", state.PID)
		return exitOK
	}
	live, ok := liveInstance(state.Addr)
	if !ok {
		// The PID is alive but nothing answers on its address. Forgetting the
		// record here would leave a live process holding serve.lock with no way
		// to find it again.
		fmt.Fprintf(os.Stderr, "pid %d is alive but not answering on %s; refusing to kill or forget it\n",
			state.PID, state.Addr)
		return exitError
	}
	if state.Instance != "" && live != state.Instance {
		fmt.Fprintf(os.Stderr, "pid %d answers with a different instance; refusing to kill it\n", state.PID)
		return exitError
	}
	if err := syscall.Kill(state.PID, syscall.SIGTERM); err != nil {
		fmt.Fprintln(os.Stderr, "stop:", err)
		return exitError
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && processAlive(state.PID) {
		time.Sleep(100 * time.Millisecond)
	}
	if processAlive(state.PID) {
		fmt.Fprintf(os.Stderr, "pid %d did not exit within 10s; it was left running\n", state.PID)
		return exitError
	}
	_ = os.Remove(serveStatePath(dir))
	fmt.Printf("stopped pid=%d\n", state.PID)
	fmt.Println("in-flight goal runs keep running: they are detached and only stopped by `kernel goal cancel`")
	return exitOK
}

// ensureStateDB creates an empty state database so the cockpit has something to
// read before the first objective exists. It only bootstraps the file: task and
// objective state is still written exclusively by `kernel goal ...`.
func ensureStateDB(dir string) error {
	path := filepath.Join(dir, "kernel.db")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	st, err := store.Open(path)
	if err != nil {
		return err
	}
	return st.Close()
}

func serveForeground(dir, addr string, control bool) int {
	serveInstance = os.Getenv("KERNEL_SERVE_INSTANCE")
	// One server per state dir, independent of PID files: the lock dies with the
	// process, so a stale serve.json cannot block a restart.
	lock, lerr := store.AcquireLock(filepath.Join(dir, "serve.lock"))
	if lerr != nil {
		if errors.Is(lerr, store.ErrLocked) {
			fmt.Fprintln(os.Stderr, "another server already serves this state dir")
		} else {
			fmt.Fprintln(os.Stderr, "serve lock:", lerr)
		}
		return exitError
	}
	defer lock.Release()
	if err := ensureStateDB(dir); err != nil {
		fmt.Fprintln(os.Stderr, "state:", err)
		return exitError
	}
	ws := newWorkspace(dir)
	cstore, err := conversation.Open(filepath.Join(dir, "conversations.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "conversation store:", err)
		return exitError
	}
	defer cstore.Close()
	settings := modelcfg.Load(dir)
	defaultModel := settings.Chat
	if defaultModel == "" {
		defaultModel = settings.Worker
	}
	if defaultModel == "" {
		defaultModel = "deepseek/deepseek-flash"
	}
	chatMgr := chat.New(chat.Config{Bin: os.Getenv("OPENCODE_BIN"), Root: dir, DefaultModel: defaultModel,
		ProjectContext: ws.ProjectContextText}, cstore)
	defer chatMgr.Close()
	mux, ctl, err := buildMux(ws, control, chatMgr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "control:", err)
		return exitError
	}
	if ctl != nil && os.Getenv("KERNEL_SERVE_DAEMON") != "1" {
		fmt.Printf("control token: %s (also in %s)\n", ctl.token, controlTokenFile(dir))
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		return exitError
	}
	fmt.Printf("http://%s\n", listener.Addr().String())
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      0, // chat uses SSE; per-handler deadlines still apply
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Shutdown only stops the HTTP server. Workers are separate detached
		// sessions, so stopping the client never aborts a run.
		_ = srv.Shutdown(shut)
	}()
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "serve:", err)
		return exitError
	}
	fmt.Println("stopped")
	return exitOK
}

func buildMux(ws *workspace, control bool, chatMgr *chat.Manager) (*http.ServeMux, *controlServer, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		serveDashboard(w)
	})
	mux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		serveChatPage(w)
	})
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, ws)
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		serveWorkspaceStatus(w, ws)
	})
	var ctl *controlServer
	mux.HandleFunc("/api/tasks/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
		parts := strings.Split(rest, "/")
		if rest == "" || len(parts) > 2 {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 2 {
			switch {
			case parts[1] == "explain" && r.Method == http.MethodPost && ctl != nil:
				ctl.explainTask(w, r, parts[0])
			case r.Method == http.MethodPost && ctl == nil:
				http.Error(w, "server was started without --control", http.StatusForbidden)
			default:
				http.NotFound(w, r)
			}
			return
		}
		p, ok, err := ws.resolve(r.URL.Query().Get("objective"))
		if err != nil || !ok {
			http.Error(w, "objective is required", http.StatusBadRequest)
			return
		}
		st, err := store.OpenReadOnly(filepath.Join(p.Dir, "kernel.db"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer st.Close()
		serveTask(w, st, p.Dir, domain.TaskID(parts[0]))
	})
	mux.HandleFunc("/api/diff", func(w http.ResponseWriter, r *http.Request) {
		serveDiff(w, r, ws)
	})
	mux.HandleFunc("/api/events/stream", func(w http.ResponseWriter, r *http.Request) {
		serveEventStream(w, r, ws)
	})
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		p, ok, err := ws.resolve(r.URL.Query().Get("objective"))
		if err != nil || !ok {
			http.Error(w, "objective is required", http.StatusBadRequest)
			return
		}
		st, err := store.OpenReadOnly(filepath.Join(p.Dir, "kernel.db"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer st.Close()
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		tail := r.URL.Query().Get("tail") == "1"
		serveEvents(w, st, after, limit, tail)
	})

	if control {
		controlOn = true
		self, err := os.Executable()
		if err != nil {
			return nil, nil, err
		}
		ctl = newControl(self, ws)
		if err := writeToken(ws.root, ctl.token); err != nil {
			return nil, nil, err
		}
	}

	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			serveModels(w, ws.root)
		case r.Method == http.MethodPost && ctl != nil:
			ctl.updateModels(w, r)
		case r.Method == http.MethodPost:
			http.Error(w, "server was started without --control", http.StatusForbidden)
		default:
			http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/report", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		p, ok, err := ws.resolve(r.URL.Query().Get("objective"))
		if err != nil || !ok {
			http.Error(w, "objective is required", http.StatusBadRequest)
			return
		}
		st, err := store.OpenReadOnly(filepath.Join(p.Dir, "kernel.db"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer st.Close()
		serveReport(w, st, p.Dir)
	})
	mux.HandleFunc("/api/projects", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			list, err := ws.Projects()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = writeJSON(w, list)
		case http.MethodPost:
			if ctl == nil {
				http.Error(w, "server was started without --control", http.StatusForbidden)
				return
			}
			if !ctl.authorised(r) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			var req struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			p, err := ws.CreateProjectWithDescription(req.Name, req.Description)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = writeJSON(w, p)
		default:
			http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/projects/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/projects/")
		parts := strings.SplitN(rest, "/", 2)
		if rest == "" || rest == r.URL.Path {
			http.NotFound(w, r)
			return
		}
		id := parts[0]
		action := ""
		if len(parts) == 2 {
			action = parts[1]
		}
		if r.Method == http.MethodGet && action == "" {
			info, ok, err := ws.FindProject(id)
			if err != nil || !ok {
				http.Error(w, "unknown project", http.StatusNotFound)
				return
			}
			snap, err := ws.BuildProjectSnapshot(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = writeJSON(w, map[string]any{"project": info, "snapshot": snap})
			return
		}
		if ctl == nil {
			http.Error(w, "server was started without --control", http.StatusForbidden)
			return
		}
		if !ctl.authorised(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && action == "refresh":
			snap, err := ws.ProjectSnapshot(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = writeJSON(w, snap)
		case r.Method == http.MethodPost && action == "decisions":
			var req struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			if err := ws.appendProjectDecision(id, req.Text); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			snap, err := ws.ProjectSnapshot(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = writeJSON(w, snap)
		default:
			http.Error(w, "unsupported", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/objectives", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		serveObjectiveList(w, ws)
	})
	mux.HandleFunc("/api/objectives/", func(w http.ResponseWriter, r *http.Request) {
		id, action, ok := splitObjectivePath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch {
		case r.Method == http.MethodGet && action == "metrics":
			st, _, err := ws.open(id)
			if err != nil {
				http.Error(w, "unknown objective", http.StatusNotFound)
				return
			}
			defer st.Close()
			serveMetrics(w, st)
		case r.Method == http.MethodGet && action == "progress":
			st, _, err := ws.open(id)
			if err != nil {
				http.Error(w, "unknown objective", http.StatusNotFound)
				return
			}
			defer st.Close()
			serveProgress(w, st, id)
		case r.Method == http.MethodGet && action == "":
			st, p, err := ws.open(id)
			if err != nil {
				http.Error(w, "unknown objective", http.StatusNotFound)
				return
			}
			defer st.Close()
			serveObjectiveDetail(w, st, p.Dir, id)
		case r.Method == http.MethodPost && action != "" && ctl != nil:
			ctl.objectiveAction(w, r, id, action)
		case r.Method == http.MethodPost && ctl == nil:
			http.Error(w, "server was started without --control", http.StatusForbidden)
		default:
			http.Error(w, "unsupported", http.StatusMethodNotAllowed)
		}
	})
	if ctl != nil {
		registerChatRoutes(mux, ctl, chatMgr, ws)
		mux.HandleFunc("/api/goals", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "POST only", http.StatusMethodNotAllowed)
				return
			}
			ctl.createGoal(w, r)
		})
		mux.HandleFunc("/api/jobs", ctl.jobStatus)
	}
	return mux, ctl, nil
}

func splitObjectivePath(path string) (id, action string, ok bool) {
	rest := strings.TrimPrefix(path, "/api/objectives/")
	if rest == "" || rest == path {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) > 2 {
		return "", "", false
	}
	id = parts[0]
	if id == "" {
		return "", "", false
	}
	if len(parts) == 2 {
		action = parts[1]
	}
	return id, action, true
}
