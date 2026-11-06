package chat

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"projectos/kernel/internal/conversation"
	"projectos/kernel/internal/opencodeserver"
)

func testManager(t *testing.T) (*Manager, *conversation.Store, conversation.Conversation) {
	t.Helper()
	root := t.TempDir()
	st, err := conversation.Open(filepath.Join(root, "conversations.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := New(Config{Bin: "/nonexistent-opencode", Root: root, DefaultModel: "provider/model"}, st)
	t.Cleanup(m.Close)
	c, err := m.CreateConversation("chat", "", "", "", "provider/model")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSession(c.ID, "ses_1"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.sessions["ses_1"] = c.ID
	m.mu.Unlock()
	return m, st, c
}

func event(t *testing.T, kind string, data any) opencodeserver.Event {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return opencodeserver.Event{Type: kind, Data: raw}
}

func TestChatEventsBecomeMessagesAndToolCards(t *testing.T) {
	m, st, c := testManager(t)
	m.handle(event(t, "session.execution.started", map[string]string{"sessionID": "ses_1"}))
	m.handle(event(t, "session.text.delta", map[string]string{"sessionID": "ses_1", "delta": "hel"}))
	m.handle(event(t, "session.text.ended", map[string]string{"sessionID": "ses_1", "text": "hello"}))
	m.handle(event(t, "session.tool.called", map[string]any{
		"sessionID": "ses_1", "id": "call_1", "name": "read", "input": map[string]string{"path": "a.txt"},
	}))
	m.handle(event(t, "permission.asked", map[string]any{
		"sessionID": "ses_1", "id": "per_1", "action": "edit", "resources": []string{"a.txt"},
	}))
	m.handle(event(t, "session.execution.succeeded", map[string]string{"sessionID": "ses_1"}))

	msgs, err := st.Messages(c.ID, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Role != "assistant" || msgs[0].Text != "hello" {
		t.Fatalf("messages = %+v", msgs)
	}
	got, ok, err := st.Get(c.ID)
	if err != nil || !ok || got.Status != conversation.StatusIdle {
		t.Fatalf("conversation = %+v %v %v", got, ok, err)
	}
	events, err := st.EventsAfter(c.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, ev := range events {
		kinds[ev.Kind]++
	}
	if kinds["tool"] != 1 || kinds["permission"] != 1 || kinds["assistant"] != 1 {
		t.Fatalf("events = %+v", kinds)
	}
	if kinds["text_delta"] != 0 {
		t.Fatal("streaming deltas were persisted")
	}
}

func TestChatSendFailsClosedWithoutRuntime(t *testing.T) {
	m, st, c := testManager(t)
	if _, err := m.Send(context.Background(), c.ID, "hello"); err == nil {
		t.Fatal("send succeeded without a chat runtime")
	}
	got, ok, err := st.Get(c.ID)
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if got.Status != conversation.StatusError {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestSubscribeReplaysPersistedEvents(t *testing.T) {
	m, st, c := testManager(t)
	if _, err := st.AppendEvent(c.ID, "tool", map[string]string{"id": "1"}); err != nil {
		t.Fatal(err)
	}
	ch, cancel, err := m.Subscribe(c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	select {
	case ev := <-ch:
		if ev.Kind != "tool" || ev.Seq == 0 {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no replayed event")
	}
}
