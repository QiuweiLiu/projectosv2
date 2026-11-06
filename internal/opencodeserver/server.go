// Package opencodeserver owns a private OpenCode v2 server for interactive
// chat. It is deliberately separate from the task executor: chat sessions must
// never write Task/Run state, and the kernel's CLI backend remains the stable
// path for managed work.
package opencodeserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	Bin            string
	Host           string
	StartupTimeout time.Duration
}

type Server struct {
	cmd      *exec.Cmd
	baseURL  string
	password string
	done     chan struct{}
	mu       sync.Mutex
	logs     []string
}

type Session struct {
	ID    string `json:"id"`
	Model struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerID"`
		Variant    string `json:"variant"`
	} `json:"model"`
	Title string `json:"title"`
}

type Message struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
	Time struct {
		Created int64 `json:"created"`
	} `json:"time"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Tokens struct {
		Input  int `json:"input"`
		Output int `json:"output"`
	} `json:"tokens"`
}

type PermissionRequest struct {
	ID        string   `json:"id"`
	SessionID string   `json:"sessionID"`
	Action    string   `json:"action"`
	Resources []string `json:"resources"`
	Message   string   `json:"message"`
}

type Event struct {
	ID      string          `json:"id"`
	Created int64           `json:"created"`
	Type    string          `json:"type"`
	Data    json.RawMessage `json:"data"`
	Durable struct {
		AggregateID string `json:"aggregateID"`
		Seq         int64  `json:"seq"`
		Version     int    `json:"version"`
	} `json:"durable"`
}

// ChatEventTypes is the minimum subscription for a chat workspace: text deltas,
// tool state, execution state and permission requests. It intentionally omits
// token-level reasoning and unrelated server events.
var ChatEventTypes = []string{
	"session.execution.started",
	"session.execution.succeeded",
	"session.execution.failed",
	"session.execution.interrupted",
	"session.text.started",
	"session.text.delta",
	"session.text.ended",
	"session.tool.called",
	"session.tool.success",
	"session.tool.failed",
	"permission.asked",
	"permission.replied",
	"session.idle",
}

func Start(ctx context.Context, cfg Config) (*Server, error) {
	if cfg.Bin == "" {
		cfg.Bin = "opencode"
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = 20 * time.Second
	}
	port, err := freePort(cfg.Host)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(cfg.Bin, "serve", "--hostname", cfg.Host, "--port", strconv.Itoa(port))
	// Never inherit a password from the surrounding shell: the private chat
	// server must use the password it generates, which never leaves memory.
	env := make([]string, 0, len(os.Environ()))
	for _, pair := range os.Environ() {
		if strings.HasPrefix(pair, "OPENCODE_PASSWORD=") {
			continue
		}
		env = append(env, pair)
	}
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &Server{cmd: cmd, baseURL: fmt.Sprintf("http://%s:%d", cfg.Host, port), done: make(chan struct{})}
	password := make(chan string, 1)
	go s.scan(pr, password)
	go func() {
		_ = cmd.Wait()
		_ = pw.Close()
		close(s.done)
	}()

	timer := time.NewTimer(cfg.StartupTimeout)
	defer timer.Stop()
	select {
	case pw := <-password:
		s.password = pw
	case <-timer.C:
		s.Stop()
		return nil, fmt.Errorf("opencode server did not print a password: %s", s.tail())
	case <-ctx.Done():
		s.Stop()
		return nil, ctx.Err()
	}
	deadline := time.Now().Add(cfg.StartupTimeout)
	for time.Now().Before(deadline) {
		if s.healthy(ctx) {
			return s, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.Stop()
	return nil, fmt.Errorf("opencode server did not become ready: %s", s.tail())
}

func freePort(host string) (int, error) {
	l, err := net.Listen("tcp", host+":0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func (s *Server) scan(r io.Reader, password chan<- string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.Contains(line, "server password") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				select {
				case password <- fields[len(fields)-1]:
				default:
				}
				s.addLog("server password: [redacted]")
				continue
			}
		}
		s.addLog(line)
	}
}

func (s *Server) addLog(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, line)
	if len(s.logs) > 60 {
		s.logs = s.logs[len(s.logs)-60:]
	}
}

func (s *Server) tail() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.logs, " | ")
}

func (s *Server) healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/api/session?limit=1", nil)
	if err != nil {
		return false
	}
	req.SetBasicAuth("opencode", s.password)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (s *Server) Alive() bool {
	if s == nil || s.done == nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *Server) Stop() {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
		}
	}
}

func (s *Server) doJSON(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.SetBasicAuth("opencode", s.password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("opencode %s %s: %s: %s", method, path, resp.Status, clip(string(raw), 500))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return nil
}

func parseModel(ref string) (map[string]string, error) {
	ref = strings.TrimSpace(ref)
	provider, id, ok := strings.Cut(ref, "/")
	if !ok || provider == "" || id == "" {
		return nil, fmt.Errorf("model %q must be provider/model", ref)
	}
	return map[string]string{"providerID": provider, "id": id}, nil
}

func (s *Server) CreateSession(ctx context.Context, title, model, dir string) (Session, error) {
	return s.CreateSessionWithPolicy(ctx, title, model, dir,
		[]map[string]string{{"action": "*", "resource": "*", "effect": "ask"}})
}

func (s *Server) CreateSessionWithPolicy(ctx context.Context, title, model, dir string, permissions []map[string]string) (Session, error) {
	m, err := parseModel(model)
	if err != nil {
		return Session{}, err
	}
	var out struct {
		Data Session `json:"data"`
	}
	err = s.doJSON(ctx, http.MethodPost, "/api/session", map[string]any{
		"title":       title,
		"model":       m,
		"location":    map[string]string{"directory": dir},
		"permissions": permissions,
	}, &out)
	return out.Data, err
}

// Prompt sends a user message and returns the provider message id. The caller
// can supply a stable local id so a later history sync can match the same row.
func (s *Server) Prompt(ctx context.Context, sessionID, text, messageID string) (string, error) {
	body := map[string]string{"text": text}
	if messageID != "" {
		body["id"] = messageID
	}
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := s.doJSON(ctx, http.MethodPost, "/api/session/"+url.PathEscape(sessionID)+"/prompt", body, &out); err != nil {
		return "", err
	}
	return out.Data.ID, nil
}

type MessagePage struct {
	Data   []Message `json:"data"`
	Cursor struct {
		Previous string `json:"previous"`
		Next     string `json:"next"`
	} `json:"cursor"`
}

func (s *Server) Messages(ctx context.Context, sessionID string, limit int) ([]Message, error) {
	page, err := s.MessagesPage(ctx, sessionID, limit, "", "")
	return page.Data, err
}

func (s *Server) MessagesPage(ctx context.Context, sessionID string, limit int, cursor, order string) (MessagePage, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if order != "" {
		q.Set("order", order)
	}
	var out MessagePage
	err := s.doJSON(ctx, http.MethodGet,
		"/api/session/"+url.PathEscape(sessionID)+"/message?"+q.Encode(), nil, &out)
	return out, err
}

func (s *Server) PendingPermissions(ctx context.Context) ([]PermissionRequest, error) {
	var out struct {
		Data []PermissionRequest `json:"data"`
	}
	err := s.doJSON(ctx, http.MethodGet, "/api/permission/request", nil, &out)
	return out.Data, err
}

func (s *Server) Interrupt(ctx context.Context, sessionID string) error {
	return s.doJSON(ctx, http.MethodPost, "/api/session/"+url.PathEscape(sessionID)+"/interrupt", nil, nil)
}

func (s *Server) ReplyPermission(ctx context.Context, sessionID, requestID, decision, message string) error {
	body := map[string]any{"decision": decision}
	if message != "" {
		body["message"] = message
	}
	return s.doJSON(ctx, http.MethodPost,
		"/api/session/"+url.PathEscape(sessionID)+"/permission/"+url.PathEscape(requestID)+"/reply", body, nil)
}

type EventStream struct {
	resp    *http.Response
	scanner *bufio.Scanner
}

func (s *Server) Subscribe(ctx context.Context, types []string) (*EventStream, error) {
	q := url.Values{}
	for _, t := range types {
		q.Add("types", t)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/api/event?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth("opencode", s.password)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		resp.Body.Close()
		return nil, fmt.Errorf("event stream: %s: %s", resp.Status, clip(string(raw), 300))
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &EventStream{resp: resp, scanner: scanner}, nil
}

// Next returns the next SSE data event. Heartbeats and comments are skipped.
func (e *EventStream) Next() (Event, error) {
	var data []string
	for e.scanner.Scan() {
		line := e.scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}
		if line == "" && len(data) > 0 {
			var ev Event
			raw := strings.Join(data, "\n")
			data = nil
			if err := json.Unmarshal([]byte(raw), &ev); err != nil {
				continue
			}
			return ev, nil
		}
	}
	if err := e.scanner.Err(); err != nil {
		return Event{}, err
	}
	return Event{}, io.EOF
}

func (e *EventStream) Close() error {
	if e == nil || e.resp == nil {
		return nil
	}
	return e.resp.Body.Close()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
