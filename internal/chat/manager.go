// Package chat connects persistent conversations to OpenCode sessions. It owns
// no Task/Run state: a chat can discuss work, but only the Kernel API can change
// a plan or accept an artifact.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"projectos/kernel/internal/conversation"
	"projectos/kernel/internal/opencodeserver"
)

var (
	ErrBusy     = errors.New("conversation is already running")
	ErrNoServer = errors.New("chat runtime is unavailable")
)

type Config struct {
	Bin          string
	Root         string // workspace root
	DefaultModel string
}

type Manager struct {
	cfg   Config
	store *conversation.Store

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	server    *opencodeserver.Server
	serverErr error
	starting  chan struct{}
	subs      map[string]map[chan conversation.Event]struct{}
	sessions  map[string]string // opencode session -> conversation
	active    map[string]bool
	eventSeen int
}

func New(cfg Config, store *conversation.Store) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{cfg: cfg, store: store, ctx: ctx, cancel: cancel,
		subs:     map[string]map[chan conversation.Event]struct{}{},
		sessions: map[string]string{}, active: map[string]bool{}}
}

func (m *Manager) Close() {
	m.cancel()
	m.mu.Lock()
	srv := m.server
	m.server = nil
	m.mu.Unlock()
	if srv != nil {
		srv.Stop()
	}
}

func (m *Manager) CreateConversation(title, projectID, objectiveID, dir, model string) (conversation.Conversation, error) {
	if strings.TrimSpace(model) == "" {
		model = m.cfg.DefaultModel
	}
	c, err := m.store.Create(title, projectID, objectiveID, dir, model)
	if err != nil {
		return conversation.Conversation{}, err
	}
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join(m.cfg.Root, "chats", c.ID)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return conversation.Conversation{}, err
	}
	if err := m.store.SetDir(c.ID, dir); err != nil {
		return conversation.Conversation{}, err
	}
	c.Dir = dir
	return c, nil
}

func (m *Manager) List(limit int) ([]conversation.Summary, error) {
	return m.store.List(limit)
}

func (m *Manager) Get(ctx context.Context, id string, limit int, before string) (conversation.Conversation, []conversation.Message, error) {
	c, ok, err := m.store.Get(id)
	if err != nil || !ok {
		return c, nil, err
	}
	// Best-effort resync when the app was closed while a reply was in flight.
	if c.SessionID != "" {
		_ = m.Sync(ctx, c)
	}
	msgs, err := m.store.Messages(id, limit, before)
	return c, msgs, err
}

func (m *Manager) SetModel(id, model string) error {
	return m.store.SetModel(id, model)
}

func (m *Manager) ensureServer(ctx context.Context) (*opencodeserver.Server, error) {
	m.mu.Lock()
	if m.server != nil && m.server.Alive() {
		s := m.server
		m.mu.Unlock()
		return s, nil
	}
	if m.server != nil {
		// The private OpenCode process died. Drop it so the next request starts
		// a fresh runtime; conversations keep their session ids and resync.
		old := m.server
		m.server = nil
		m.mu.Unlock()
		old.Stop()
		m.mu.Lock()
	}
	if m.starting != nil {
		ch := m.starting
		m.mu.Unlock()
		select {
		case <-ch:
			m.mu.Lock()
			s, err := m.server, m.serverErr
			m.mu.Unlock()
			return s, err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	m.starting = make(chan struct{})
	m.mu.Unlock()

	startCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	s, err := opencodeserver.Start(startCtx, opencodeserver.Config{Bin: m.cfg.Bin})

	m.mu.Lock()
	m.server, m.serverErr = s, err
	close(m.starting)
	m.starting = nil
	m.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoServer, err)
	}
	m.loadSessions()
	go m.eventLoop()
	go m.resyncAll()
	return s, nil
}

func (m *Manager) loadSessions() {
	list, err := m.store.List(200)
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range list {
		if c.SessionID != "" {
			m.sessions[c.SessionID] = c.ID
		}
	}
}

// ensureSession creates an OpenCode session lazily. A conversation can sit in
// the database without ever touching the runtime.
func (m *Manager) ensureSession(ctx context.Context, srv *opencodeserver.Server, c conversation.Conversation) (string, error) {
	if c.SessionID != "" {
		return c.SessionID, nil
	}
	dir := c.Dir
	if dir == "" {
		dir = filepath.Join(m.cfg.Root, "chats", c.ID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if err := m.store.SetDir(c.ID, dir); err != nil {
			return "", err
		}
	}
	title := c.Title
	if title == "" {
		title = "ProjectOS chat"
	}
	sess, err := srv.CreateSession(ctx, title, c.Model, dir)
	if err != nil {
		return "", err
	}
	if err := m.store.SetSession(c.ID, sess.ID); err != nil {
		return "", err
	}
	m.mu.Lock()
	m.sessions[sess.ID] = c.ID
	m.mu.Unlock()
	return sess.ID, nil
}

func (m *Manager) Send(ctx context.Context, id, text string) (conversation.Message, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return conversation.Message{}, fmt.Errorf("message is empty")
	}
	c, ok, err := m.store.Get(id)
	if err != nil {
		return conversation.Message{}, err
	}
	if !ok {
		return conversation.Message{}, fmt.Errorf("unknown conversation")
	}
	m.mu.Lock()
	if m.active[id] {
		m.mu.Unlock()
		return conversation.Message{}, ErrBusy
	}
	m.active[id] = true
	m.mu.Unlock()
	fail := func(err error) (conversation.Message, error) {
		m.mu.Lock()
		delete(m.active, id)
		m.mu.Unlock()
		_ = m.store.SetStatus(id, conversation.StatusError, err.Error())
		_, _ = m.persist(id, "error", map[string]string{"message": err.Error()})
		return conversation.Message{}, err
	}

	srv, err := m.ensureServer(ctx)
	if err != nil {
		return fail(err)
	}
	sessionID, err := m.ensureSession(ctx, srv, c)
	if err != nil {
		return fail(err)
	}
	msg, err := m.store.AppendMessage(id, "user", text)
	if err != nil {
		return fail(err)
	}
	_, _ = m.persist(id, "user", map[string]string{"text": text, "messageId": msg.ID})
	_ = m.store.SetStatus(id, conversation.StatusRunning, "")
	if err := srv.Prompt(ctx, sessionID, text); err != nil {
		// The server may have been restarted and no longer knows the session.
		// Recreate once, preserving the conversation title and workspace.
		if strings.Contains(err.Error(), "404") {
			_ = m.store.SetSession(id, "")
			sessionID, err = m.ensureSession(ctx, srv, conversation.Conversation{
				ID: id, Title: c.Title, Model: c.Model, Dir: c.Dir,
			})
			if err == nil {
				err = srv.Prompt(ctx, sessionID, text)
			}
		}
		if err != nil {
			return fail(err)
		}
	}
	return msg, nil
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	c, ok, err := m.store.Get(id)
	if err != nil || !ok {
		return fmt.Errorf("unknown conversation")
	}
	if c.SessionID == "" {
		return nil
	}
	srv, err := m.ensureServer(ctx)
	if err != nil {
		return err
	}
	if err := srv.Interrupt(ctx, c.SessionID); err != nil {
		return err
	}
	_ = m.store.SetStatus(id, conversation.StatusInterrupted, "")
	_, _ = m.persist(id, "status", map[string]string{"status": "interrupted"})
	return nil
}

func (m *Manager) ReplyPermission(ctx context.Context, id, requestID, decision, message string) error {
	c, ok, err := m.store.Get(id)
	if err != nil || !ok || c.SessionID == "" {
		return fmt.Errorf("conversation has no active session")
	}
	srv, err := m.ensureServer(ctx)
	if err != nil {
		return err
	}
	if decision != "once" && decision != "always" && decision != "reject" {
		return fmt.Errorf("decision must be once, always or reject")
	}
	if err := srv.ReplyPermission(ctx, c.SessionID, requestID, decision, message); err != nil {
		return err
	}
	_, _ = m.persist(id, "permission_replied", map[string]string{"requestId": requestID, "decision": decision})
	return nil
}

func (m *Manager) Subscribe(id string, after int64) (<-chan conversation.Event, func(), error) {
	ch := make(chan conversation.Event, 256)
	m.mu.Lock()
	// Query and enqueue the replay while holding the publish lock. Otherwise a
	// live event can arrive before its older replay and the browser's cursor
	// would skip the replay.
	replay, err := m.store.EventsAfter(id, after, 200)
	if err != nil {
		m.mu.Unlock()
		close(ch)
		return nil, nil, err
	}
	for _, ev := range replay {
		ch <- ev
	}
	if m.subs[id] == nil {
		m.subs[id] = map[chan conversation.Event]struct{}{}
	}
	m.subs[id][ch] = struct{}{}
	m.mu.Unlock()
	cancel := func() {
		m.mu.Lock()
		if _, ok := m.subs[id][ch]; ok {
			delete(m.subs[id], ch)
			// Do not close: a consumer may still be reading queued replay.
		}
		m.mu.Unlock()
	}
	return ch, cancel, nil
}

func (m *Manager) publishLive(id, kind string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	m.publish(conversation.Event{ConversationID: id, Kind: kind, Data: raw, At: time.Now().UTC()})
}

func (m *Manager) persist(id, kind string, data any) (conversation.Event, error) {
	ev, err := m.store.AppendEvent(id, kind, data)
	if err != nil {
		return ev, err
	}
	m.mu.Lock()
	m.eventSeen++
	prune := m.eventSeen%50 == 0
	m.mu.Unlock()
	if prune {
		_ = m.store.PruneEvents(500)
	}
	m.publish(ev)
	return ev, nil
}

func (m *Manager) publish(ev conversation.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.subs[ev.ConversationID] {
		select {
		case ch <- ev:
		default:
			// A slow browser must not block the runtime. It can reload history.
		}
	}
}

func (m *Manager) eventLoop() {
	for {
		select {
		case <-m.ctx.Done():
			return
		default:
		}
		srv := m.currentServer()
		if srv == nil {
			return
		}
		stream, err := srv.Subscribe(m.ctx, opencodeserver.ChatEventTypes)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		for {
			ev, err := stream.Next()
			if err != nil {
				break
			}
			m.handle(ev)
		}
		stream.Close()
		// Events missed while disconnected are repaired by Sync on next open.
		go m.resyncAll()
		time.Sleep(time.Second)
	}
}

func (m *Manager) currentServer() *opencodeserver.Server {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.server
}

func (m *Manager) handle(ev opencodeserver.Event) {
	var base struct {
		SessionID          string          `json:"sessionID"`
		AssistantMessageID string          `json:"assistantMessageID"`
		Delta              string          `json:"delta"`
		Text               string          `json:"text"`
		ID                 string          `json:"id"`
		Name               string          `json:"name"`
		Input              json.RawMessage `json:"input"`
		Action             string          `json:"action"`
		Resources          []string        `json:"resources"`
		Message            string          `json:"message"`
	}
	_ = json.Unmarshal(ev.Data, &base)
	m.mu.Lock()
	convID := m.sessions[base.SessionID]
	m.mu.Unlock()
	if convID == "" {
		return
	}
	switch ev.Type {
	case "session.text.delta":
		// Deltas are live-only; persisting every token would turn the event log
		// into an unbounded write amplifier. The final text is stored below.
		m.publishLive(convID, "text_delta", map[string]string{"delta": base.Delta})
	case "session.text.ended":
		if strings.TrimSpace(base.Text) == "" {
			return
		}
		if _, err := m.store.AppendMessage(convID, "assistant", base.Text); err != nil {
			return
		}
		_ = m.store.PruneMessages(convID, 1000)
		_, _ = m.persist(convID, "assistant", map[string]string{"text": base.Text, "messageId": base.AssistantMessageID})
	case "session.tool.called":
		_, _ = m.persist(convID, "tool", map[string]any{"id": base.ID, "name": base.Name, "input": base.Input})
	case "session.tool.success":
		_, _ = m.persist(convID, "tool_result", map[string]any{"id": base.ID, "status": "ok"})
	case "session.tool.failed":
		_, _ = m.persist(convID, "tool_result", map[string]any{"id": base.ID, "status": "failed", "message": base.Text})
	case "permission.asked":
		_, _ = m.persist(convID, "permission", map[string]any{
			"requestId": base.ID, "action": base.Action, "resources": base.Resources, "message": base.Message,
		})
	case "permission.replied":
		_, _ = m.persist(convID, "permission_replied", map[string]string{"requestId": base.ID})
	case "session.execution.started":
		_ = m.store.SetStatus(convID, conversation.StatusRunning, "")
		_, _ = m.persist(convID, "status", map[string]string{"status": "running"})
	case "session.execution.succeeded":
		m.setIdle(convID, conversation.StatusIdle, "")
	case "session.execution.interrupted":
		m.setIdle(convID, conversation.StatusInterrupted, "")
	case "session.execution.failed":
		m.setIdle(convID, conversation.StatusError, base.Text)
	case "session.idle":
		m.setIdle(convID, conversation.StatusIdle, "")
	}
}

func (m *Manager) setIdle(id string, status conversation.Status, errText string) {
	m.mu.Lock()
	delete(m.active, id)
	m.mu.Unlock()
	_ = m.store.SetStatus(id, status, errText)
	_, _ = m.persist(id, "status", map[string]string{"status": string(status), "error": errText})
}

// Sync repairs a missed final message after the desktop app or event stream was
// disconnected. It only imports user/assistant text, never tool internals.
func (m *Manager) Sync(ctx context.Context, c conversation.Conversation) error {
	if c.SessionID == "" {
		return nil
	}
	srv := m.currentServer()
	if srv == nil {
		return nil
	}
	msgs, err := srv.Messages(ctx, c.SessionID, 100)
	if err != nil {
		return err
	}
	existing, err := m.store.Messages(c.ID, 500, "")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, msg := range existing {
		seen[msg.Role+"\x00"+msg.Text] = true
	}
	for _, msg := range msgs {
		switch msg.Type {
		case "user":
			if msg.Text != "" && !seen["user\x00"+msg.Text] {
				_, _ = m.store.AppendMessage(c.ID, "user", msg.Text)
				seen["user\x00"+msg.Text] = true
			}
		case "assistant":
			var text strings.Builder
			for _, part := range msg.Content {
				if part.Type == "text" {
					text.WriteString(part.Text)
				}
			}
			if text.Len() > 0 && !seen["assistant\x00"+text.String()] {
				_, _ = m.store.AppendMessage(c.ID, "assistant", text.String())
				seen["assistant\x00"+text.String()] = true
			}
		}
	}
	return nil
}

func (m *Manager) resyncAll() {
	list, err := m.store.List(50)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
	defer cancel()
	for _, c := range list {
		if c.SessionID != "" && c.Status != conversation.StatusRunning {
			_ = m.Sync(ctx, c.Conversation)
		}
	}
}

// Active reports whether a conversation currently has an in-flight turn.
func (m *Manager) Active(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[id]
}

// ServerError is surfaced by the HTTP layer when chat is unavailable.
func (m *Manager) ServerError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.serverErr
}
