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
	type candidate struct {
		id    string
		delta int64
	}
	var candidates []candidate
	for _, msg := range msgs {
		if msg.Type != "user" || msg.ID == "" {
			continue
		}
		serverText := strings.TrimSpace(msg.Text)
		// Exact, or the exact user text after the injected project context.
		// Arbitrary substring matching made a short message ("ok", "你好")
		// collide with a long context block.
		if serverText != text &&
			!(strings.HasPrefix(serverText, "Project context") && strings.HasSuffix(serverText, "\n\n"+text)) {
			continue
		}
		if target.CreatedAt.IsZero() {
			candidates = append(candidates, candidate{id: msg.ID})
			continue
		}
		delta := msg.Time.Created - target.CreatedAt.UnixMilli()
		if delta < 0 {
			delta = -delta
		}
		if delta <= int64((10*time.Minute)/time.Millisecond) {
			candidates = append(candidates, candidate{id: msg.ID, delta: delta})
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	if target.CreatedAt.IsZero() {
		if len(candidates) == 1 {
			return candidates[0].id
		}
		return ""
	}
	minDelta := candidates[0].delta
	for _, c := range candidates[1:] {
		if c.delta < minDelta {
			minDelta = c.delta
		}
	}
	// Duplicate text is common ("ok", "继续"). If two copies are within a
	// clock-skew window of the nearest match, forking either one may cut the
	// wrong boundary; ask for a local-only retry instead.
	const ambiguityWindow = int64((2 * time.Second) / time.Millisecond)
	best := ""
	matches := 0
	for _, c := range candidates {
		if c.delta <= minDelta+ambiguityWindow {
			matches++
			if c.delta == minDelta {
				best = c.id
			}
		}
	}
	if matches != 1 {
		return ""
	}
	return best
}

func listServerMessages(ctx context.Context, srv *opencodeserver.Server, sessionID string) []opencodeserver.Message {
	var out []opencodeserver.Message
	cursor := ""
	for page := 0; page < 10; page++ {
		result, err := srv.MessagesPage(ctx, sessionID, 200, cursor, "asc")
		if err != nil {
			return nil
		}
		out = append(out, result.Data...)
		if result.Cursor.Next == "" || len(result.Data) == 0 || len(out) >= 2000 {
			break
		}
		cursor = result.Cursor.Next
	}
	return out
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
	var srv *opencodeserver.Server
	// A message without a remote id never reached OpenCode (e.g. a control
	// phrase stored locally). There is no server-side turn to fork away.
	if before := strings.TrimSpace(target.RemoteID); before != "" && sessionID != "" && !localOnly {
		var serr error
		srv, serr = m.ensureServer(ctx)
		if serr != nil {
			return RewindResult{}, serr
		}
		forked, ferr := srv.ForkSession(ctx, sessionID, before)
		if errors.Is(ferr, opencodeserver.ErrForkMessageNotFound) {
			// The stored remote id can be a local id from an older build, or the
			// server may have regenerated it. Resolve the real message by text
			// and time before falling back to a local-only rewind.
			if msgs := listServerMessages(ctx, srv, sessionID); len(msgs) > 0 {
				if actual := matchServerUserMessage(msgs, target); actual != "" && actual != before {
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
	removedTarget, removed, err := m.store.RewindWithSession(conversationID, target.ID,
		sessionID, !remoteForked, conversation.StatusIdle)
	if err != nil {
		if remoteForked && srv != nil {
			// Best effort: do not leave an orphan fork when the local transaction
			// failed after the remote session was created.
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = srv.DeleteSession(cleanup, sessionID)
			cancel()
		}
		return RewindResult{}, err
	}
	removedHasRemote := false
	for _, msg := range removed {
		if strings.TrimSpace(msg.RemoteID) != "" {
			removedHasRemote = true
			break
		}
	}
	cleared := false
	if remoteForked {
		m.mu.Lock()
		if c.SessionID != "" && c.SessionID != sessionID {
			// The fork is the live session now; late events from the old one
			// must not resolve to this conversation again.
			delete(m.sessions, c.SessionID)
		}
		m.sessions[sessionID] = conversationID
		m.mu.Unlock()
	} else if removedHasRemote {
		// The store cleared session_id in the same transaction; mirror it.
		sessionID = ""
		cleared = true
		m.mu.Lock()
		delete(m.sessions, c.SessionID)
		m.mu.Unlock()
	}
	_ = cleared
	m.publishLive(conversationID, "rewind", map[string]any{
		"messageId":    removedTarget.ID,
		"removed":      len(removed),
		"remoteForked": remoteForked,
	})
	return RewindResult{RestoredText: removedTarget.Text, Removed: len(removed),
		Session: sessionID, RemoteForked: remoteForked}, nil
}
