package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SuggestObjective asks the chat model whether the latest exchange is really a
// long-running objective. A positive result is only a proposal: the user still
// confirms the handoff before an Objective exists.
func (m *Manager) suggestLog(format string, args ...any) {
	f, err := os.OpenFile(filepath.Join(m.cfg.Root, "suggest.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

func (m *Manager) SuggestObjective(ctx context.Context, conversationID, assistantMessageID string) {
	c, ok, err := m.store.Get(conversationID)
	if err != nil || !ok || c.ObjectiveID != "" {
		return
	}
	// One proposal per conversation is enough. Dismissing it does not disable
	// the explicit "转为托管目标" button, so repeated prompts are pure noise.
	if events, eerr := m.store.EventsAfter(conversationID, 0, 500); eerr == nil {
		for _, ev := range events {
			if ev.Kind == "handoff_suggestion" {
				return
			}
		}
	}
	if assistantMessageID != "" {
		if prev, seen := m.suggested.Load(conversationID); seen && prev == assistantMessageID {
			return
		}
		m.suggested.Store(conversationID, assistantMessageID)
	}

	msgs, err := m.store.Messages(conversationID, 8, "")
	if err != nil || len(msgs) < 3 {
		return
	}
	userTurns := 0
	for _, msg := range msgs {
		if msg.Role == "user" {
			userTurns++
		}
	}
	if userTurns < 2 {
		return
	}
	// Do not propose while the assistant is still gathering requirements.
	last := msgs[len(msgs)-1]
	if last.Role == "assistant" &&
		(strings.Contains(last.Text, "？") || strings.HasSuffix(strings.TrimSpace(last.Text), "?")) {
		return
	}
	var b strings.Builder
	b.WriteString(`You route a chat workspace conversation. Decide whether it should become a managed Objective handled by ProjectOS.
Return JSON only, no prose: {"objective":true|false,"confidence":0.0,"goal":"...","reason":"..."}

objective=true only when the conversation already contains a concrete goal AND at least one concrete deliverable, constraint, or acceptance condition, and the user has asked to plan or proceed. Multi-step code/file work, research with outputs, and repeated operations qualify.
objective=false for greetings, explanations, single facts, short advice, meta questions, anything already answered in one turn, and any conversation where key requirements are still missing or the assistant is asking a clarifying question.
goal: one concise sentence in the user's language; empty when objective=false.
reason: at most 20 words, in the user's language.
Do not infer requirements the user did not state.

Conversation (most recent last):
`)
	remaining := 8 << 10
	var lines []string
	for i := len(msgs) - 1; i >= 0 && remaining > 0; i-- {
		line := msgs[i].Role + ": " + msgs[i].Text + "\n"
		if len(line) > 2000 {
			line = line[:2000] + "…\n"
		}
		if len(line) > remaining {
			line = line[:remaining]
		}
		lines = append(lines, line)
		remaining -= len(line)
	}
	for i := len(lines) - 1; i >= 0; i-- {
		b.WriteString(lines[i])
	}

	srv, err := m.ensureServer(ctx)
	if err != nil {
		return
	}
	dir := filepath.Join(m.cfg.Root, "chats", "suggest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	model := c.Model
	if strings.TrimSpace(model) == "" {
		model = m.cfg.DefaultModel
	}
	// Triage should be cheap and quick; do not apply the chat reasoning variant.
	sess, err := srv.CreateSessionWithPolicy(ctx, "Objective suggestion", model, dir,
		[]map[string]string{{"action": "*", "resource": "*", "effect": "deny"}})
	if err != nil {
		return
	}
	if _, err := srv.Prompt(ctx, sess.ID, b.String(), ""); err != nil {
		return
	}

	deadline := time.Now().Add(45 * time.Second)
	text := ""
	for time.Now().Before(deadline) {
		read, err := srv.Messages(ctx, sess.ID, 20)
		if err != nil {
			return
		}
		for _, msg := range read {
			if msg.Type != "assistant" {
				continue
			}
			var out strings.Builder
			for _, part := range msg.Content {
				if part.Type == "text" {
					out.WriteString(part.Text)
				}
			}
			if out.Len() > 0 {
				text = out.String()
				break
			}
		}
		if text != "" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	raw := firstJSONObject(text)
	if raw == "" {
		return
	}
	var parsed struct {
		Objective  bool    `json:"objective"`
		Confidence float64 `json:"confidence"`
		Goal       string  `json:"goal"`
		Reason     string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		m.suggestLog("conv=%s parse error: %v raw=%s", conversationID, err, clipText(raw, 300))
		return
	}
	parsed.Goal = strings.TrimSpace(parsed.Goal)
	parsed.Reason = strings.TrimSpace(parsed.Reason)
	m.suggestLog("conv=%s objective=%v confidence=%.2f goal=%q reason=%q", conversationID,
		parsed.Objective, parsed.Confidence, clipText(parsed.Goal, 120), clipText(parsed.Reason, 120))
	if !parsed.Objective || parsed.Confidence < 0.75 || parsed.Goal == "" {
		return
	}
	_, _ = m.persist(conversationID, "handoff_suggestion", map[string]any{
		"goal":            clipText(parsed.Goal, 300),
		"reason":          clipText(parsed.Reason, 160),
		"confidence":      parsed.Confidence,
		"sourceMessageId": assistantMessageID,
	})
}

func firstJSONObject(text string) string {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return ""
	}
	return text[start : end+1]
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}
