package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"projectos/kernel/internal/opencodeserver"
)

// Forms lists the question-tool forms attached to a conversation's session.
func (m *Manager) Forms(ctx context.Context, conversationID string) ([]opencodeserver.Form, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, ok, err := m.store.Get(conversationID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("unknown conversation")
	}
	if strings.TrimSpace(c.SessionID) == "" {
		return nil, nil
	}
	srv, err := m.ensureServer(ctx)
	if err != nil {
		return nil, err
	}
	return srv.ListForms(ctx, c.SessionID)
}

// ReplyForm submits answers keyed by FormField.Key and lets the question tool
// continue the turn.
func (m *Manager) ReplyForm(ctx context.Context, conversationID, formID string, answer map[string]any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, ok, err := m.store.Get(conversationID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("unknown conversation")
	}
	if strings.TrimSpace(c.SessionID) == "" {
		return fmt.Errorf("conversation has no active session")
	}
	if strings.TrimSpace(formID) == "" {
		return fmt.Errorf("form id is required")
	}
	if len(answer) == 0 {
		return fmt.Errorf("answer is required")
	}
	srv, err := m.ensureServer(ctx)
	if err != nil {
		return err
	}
	return srv.ReplyForm(ctx, c.SessionID, formID, answer)
}
