package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"projectos/kernel/internal/conversation"
	"projectos/kernel/internal/opencodeserver"
)

// ErrRemoteGone asks the caller to confirm a local-only rewind: the message
// existed in this conversation, but the server no longer has it.
var ErrRemoteGone = errors.New("rewind_local_only")

type RewindResult struct {
	RestoredText string `json:"restoredText"`
	Removed      int    `json:"removed"`
	Session      string `json:"session"`
	// RemoteForked reports whether the OpenCode context was actually forked.
	// False means only the local transcript was rewound (the server no longer
	// knows the message).
	RemoteForked bool `json:"remoteForked"`
}

// matchServerUserMessage finds the server-side copy of a local user message.
// Older rows can carry a local id as their remote id; the first prompt may also
// be prefixed with injected project context. Match by text containment and
// closest timestamp so a wrong id does not turn into "history is gone".
func matchServerUserMessage(msgs []opencodeserver.Message, target conversation.Message) string {
	text := strings.TrimSpace(target.Text)
	if text == "" {
		return ""
	}
	best, bestDelta := "", int64((10*time.Minute)/time.Millisecond)
	for _, msg := range msgs {
		if msg.Type != "user" || msg.ID == "" {
			continue
		}
		serverText := strings.TrimSpace(msg.Text)
		if serverText != text && !strings.Contains(serverText, text) {
			continue
		}
		if target.CreatedAt.IsZero() {
			return msg.ID
		}
		delta := msg.Time.Created - target.CreatedAt.UnixMilli()
		if delta < 0 {
			delta = -delta
		}
		if delta < bestDelta {
			best, bestDelta = msg.ID, delta
		}
	}
	return best
}

// Rewind forks the OpenCode session before the selected user message, then
// removes that message and every later message/event. Files and tasks already
// produced by tools are not rolled back.
func (m *Manager) Rewind(ctx context.Context, conversationID, messageID string, localOnly bool) (RewindResult, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return RewindResult{}, fmt.Errorf("messageId is required")
	}
	c, ok, err := m.store.Get(conversationID)
	if err != nil {
		return RewindResult{}, err
	}
	if !ok {
		return RewindResult{}, fmt.Errorf("unknown conversation")
	}
	if c.Status == conversation.StatusRunning {
		return RewindResult{}, ErrBusy
	}
	m.mu.Lock()
	if m.active[conversationID] {
		m.mu.Unlock()
		return RewindResult{}, ErrBusy
	}
	m.active[conversationID] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.active, conversationID)
		m.mu.Unlock()
	}()

	target, found, err := m.store.MessageByID(conversationID, messageID)
	if err != nil {
		return RewindResult{}, err
	}
	if !found {
		return RewindResult{}, fmt.Errorf("message not found")
	}
	if target.Role != "user" {
		return RewindResult{}, fmt.Errorf("rewind must start at a user message")
	}
	sessionID := c.SessionID
	remoteForked := false
	// A message without a remote id never reached OpenCode (e.g. a control
	// phrase stored locally). There is no server-side turn to fork away.
	if before := strings.TrimSpace(target.RemoteID); before != "" && sessionID != "" && !localOnly {
		srv, serr := m.ensureServer(ctx)
		if serr != nil {
			return RewindResult{}, serr
		}
		forked, ferr := srv.ForkSession(ctx, sessionID, before)
		if errors.Is(ferr, opencodeserver.ErrForkMessageNotFound) {
			// The stored remote id can be a local id from an older build, or the
			// server may have regenerated it. Resolve the real message by text
			// and time before falling back to a local-only rewind.
			if page, perr := srv.MessagesPage(ctx, sessionID, 200, "", "asc"); perr == nil {
				if actual := matchServerUserMessage(page.Data, target); actual != "" && actual != before {
					forked, ferr = srv.ForkSession(ctx, sessionID, actual)
				}
			}
		}
		switch {
		case ferr == nil:
			sessionID = forked.ID
			remoteForked = true
		case errors.Is(ferr, opencodeserver.ErrForkMessageNotFound):
			// Deleting the whole tail without saying so would be a surprise when
			// the server lost the history. Ask for an explicit local-only retry.
			return RewindResult{}, ErrRemoteGone
		default:
			return RewindResult{}, fmt.Errorf("fork session: %w", ferr)
		}
	}
	removedTarget, removed, err := m.store.Rewind(conversationID, target.ID)
	if err != nil {
		return RewindResult{}, err
	}
	if remoteForked {
		if err := m.store.SetSession(conversationID, sessionID); err != nil {
			return RewindResult{}, err
		}
		m.mu.Lock()
		delete(m.sessions, c.SessionID)
		m.sessions[sessionID] = conversationID
		m.mu.Unlock()
	}
	_ = m.store.SetStatus(conversationID, conversation.StatusIdle, "")
	m.publishLive(conversationID, "rewind", map[string]any{
		"messageId":    removedTarget.ID,
		"removed":      len(removed),
		"remoteForked": remoteForked,
	})
	return RewindResult{RestoredText: removedTarget.Text, Removed: len(removed),
		Session: sessionID, RemoteForked: remoteForked}, nil
}
