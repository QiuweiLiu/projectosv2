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
	"errors"
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

type FormOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type FormField struct {
	Key         string       `json:"key"`
	Type        string       `json:"type"`
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description,omitempty"`
	Required    bool         `json:"required,omitempty"`
	Options     []FormOption `json:"options,omitempty"`
	Default     any          `json:"default,omitempty"`
	MinItems    *int         `json:"minItems,omitempty"`
	MaxItems    *int         `json:"maxItems,omitempty"`
	Custom      bool         `json:"custom,omitempty"`
}

type Form struct {
	ID        string          `json:"id"`
	SessionID string          `json:"sessionID"`
	Title     string          `json:"title"`
	Fields    []FormField     `json:"fields"`
	State     json.RawMessage `json:"state,omitempty"`
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

// ChatEventTypes is the minimum subscription for a chat workspace: text and
// reasoning deltas, tool state, execution state and permission requests. It
// omits unrelated server events.
var ChatEventTypes = []string{
	"session.execution.started",
	"session.execution.succeeded",
	"session.execution.failed",
	"session.execution.interrupted",
	"session.text.started",
	"session.text.delta",
	"session.text.ended",
	"session.reasoning.started",
	"session.reasoning.delta",
	"session.reasoning.ended",
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

	deadline := time.Now().Add(cfg.StartupTimeout)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case pw := <-password:
		s.password = pw
	case <-timer.C:
		s.Stop()
		return nil, fmt.Errorf("opencode server did not print a password: %s", s.tail())
	case <-s.done:
		s.Stop()
		return nil, fmt.Errorf("opencode server exited before printing a password: %s", s.tail())
	case <-ctx.Done():
		s.Stop()
		return nil, ctx.Err()
	}
	for time.Now().Before(deadline) {
		select {
		case <-s.done:
			s.Stop()
			return nil, fmt.Errorf("opencode server exited during startup: %s", s.tail())
		case <-ctx.Done():
			s.Stop()
			return nil, ctx.Err()
		default:
		}
		if s.healthy(ctx) {
			return s, nil
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-s.done:
			s.Stop()
			return nil, fmt.Errorf("opencode server exited during startup: %s", s.tail())
		case <-ctx.Done():
			s.Stop()
			return nil, ctx.Err()
		}
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
	// Drain the child's stdout/stderr even for very long lines. A Scanner with
	// a 1MB cap stops on ErrTooLong and leaves the io.Pipe writer blocked
	// forever, deadlocking the whole server.
	if closer, ok := r.(io.Closer); ok {
		defer closer.Close()
	}
	const maxLine = 1 << 20
	reader := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	overflow := false
	for {
		frag, err := reader.ReadSlice('\n')
		if !overflow {
			if len(line)+len(frag) > maxLine {
				line = append(line, frag[:maxLine-len(line)]...)
				overflow = true
			} else {
				line = append(line, frag...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if len(line) > 0 {
			text := strings.TrimSpace(string(line))
			if overflow {
				text += " …[truncated]"
			}
			if text != "" {
				if strings.Contains(text, "server password") {
					fields := strings.Fields(text)
					if len(fields) > 0 {
						select {
						case password <- fields[len(fields)-1]:
						default:
						}
						s.addLog("server password: [redacted]")
					}
				} else {
					s.addLog(text)
				}
			}
		}
		line = line[:0]
		overflow = false
		if err != nil {
			return
		}
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

// HTTPError keeps the upstream status code so callers can react to 404 (gone)
// without string matching a formatted message.
type HTTPError struct {
	StatusCode int
	Method     string
	Path       string
	Status     string
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("opencode %s %s: %s: %s", e.Method, e.Path, e.Status, e.Body)
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
		return &HTTPError{StatusCode: resp.StatusCode, Method: method, Path: path,
			Status: resp.Status, Body: clip(string(raw), 500)}
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
	provider, rest, ok := strings.Cut(ref, "/")
	if !ok || provider == "" || rest == "" {
		return nil, fmt.Errorf("model %q must be provider/model", ref)
	}
	id, variant, hasVariant := strings.Cut(rest, "#")
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("model %q must be provider/model", ref)
	}
	model := map[string]string{"providerID": provider, "id": id}
	if hasVariant {
		variant = strings.TrimSpace(variant)
		if variant == "" {
			return nil, fmt.Errorf("model %q has an empty variant", ref)
		}
		model["variant"] = variant
	}
	return model, nil
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

// ForkSession creates a child session that copies the projected history before
// the given message. The old session is left untouched.
// ErrForkMessageNotFound reports a fork target the server no longer knows:
// the session was recreated, or the message never reached OpenCode.
var ErrForkMessageNotFound = errors.New("fork message not found")

func (s *Server) ForkSession(ctx context.Context, sessionID, before string) (Session, error) {
	var out struct {
		Data Session `json:"data"`
	}
	body := map[string]any{"before": before}
	if err := s.doJSON(ctx, http.MethodPost,
		"/api/session/"+url.PathEscape(sessionID)+"/fork", body, &out); err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.StatusCode == http.StatusNotFound &&
			(strings.Contains(he.Body, "MessageNotFound") || strings.Contains(strings.ToLower(he.Body), "message not found")) {
			return Session{}, ErrForkMessageNotFound
		}
		return Session{}, err
	}
	return out.Data, nil
}

// ListForms returns pending (and recent) forms attached to a session. The
// question tool uses these forms to ask the user for choices.
func (s *Server) ListForms(ctx context.Context, sessionID string) ([]Form, error) {
	var out struct {
		Data []Form `json:"data"`
	}
	if err := s.doJSON(ctx, http.MethodGet,
		"/api/session/"+url.PathEscape(sessionID)+"/form", nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// ReplyForm submits one answer map keyed by FormField.Key.
func (s *Server) ReplyForm(ctx context.Context, sessionID, formID string, answer map[string]any) error {
	body := map[string]any{"answer": answer}
	return s.doJSON(ctx, http.MethodPost,
		"/api/session/"+url.PathEscape(sessionID)+"/form/"+url.PathEscape(formID)+"/reply", body, nil)
}

// SetInstruction attaches a durable instruction entry to a session. The
// question is announced to the model at the next step boundary.
func (s *Server) SetInstruction(ctx context.Context, sessionID, key string, value any) error {
	return s.doJSON(ctx, http.MethodPut,
		"/api/experimental/session/"+url.PathEscape(sessionID)+"/instructions/entries/"+url.PathEscape(key),
		map[string]any{"value": value}, nil)
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

// ErrPermissionNotFound reports a reply to a permission request the server no
// longer knows about: it expired, was answered elsewhere, or the session was
// replaced. Callers should drop the stale prompt instead of retrying.
var ErrPermissionNotFound = errors.New("permission not found")

func (s *Server) ReplyPermission(ctx context.Context, sessionID, requestID, decision, message string) error {
	body := map[string]any{"decision": decision}
	if message != "" {
		body["message"] = message
	}
	err := s.doJSON(ctx, http.MethodPost,
		"/api/session/"+url.PathEscape(sessionID)+"/permission/"+url.PathEscape(requestID)+"/reply", body, nil)
	var he *HTTPError
	if errors.As(err, &he) && he.StatusCode == http.StatusNotFound {
		return ErrPermissionNotFound
	}
	return err
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
