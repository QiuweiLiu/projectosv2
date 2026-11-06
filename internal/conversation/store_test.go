package conversation

import (
	"path/filepath"
	"testing"
)

func TestConversationRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "conversations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	c, err := st.Create("聊聊项目", "", "obj-1", "/tmp/chat", "opencode-go/deepseek-v4.1-flash")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == "" || c.Status != StatusIdle {
		t.Fatalf("conversation = %+v", c)
	}
	if _, err := st.AppendMessage(c.ID, "user", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(c.ID, "assistant", "hi"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSession(c.ID, "ses_1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStatus(c.ID, StatusRunning, ""); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.Get(c.ID)
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if got.SessionID != "ses_1" || got.Status != StatusRunning {
		t.Fatalf("conversation = %+v", got)
	}
	msgs, err := st.Messages(c.ID, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Text != "hello" || msgs[1].Text != "hi" {
		t.Fatalf("messages = %+v", msgs)
	}
	list, err := st.List(10)
	if err != nil || len(list) != 1 || list[0].LastMessage != "hi" {
		t.Fatalf("list = %+v %v", list, err)
	}
}

func TestConversationEventsReplayAndPrune(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "conversations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := st.Create("events", "", "", "/tmp/chat", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := st.AppendEvent(c.ID, "tool", map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := st.EventsAfter(c.ID, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Seq != 3 {
		t.Fatalf("events = %+v", events)
	}
	if err := st.PruneEvents(2); err != nil {
		t.Fatal(err)
	}
	events, err = st.EventsAfter(c.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Seq != 4 {
		t.Fatalf("after prune = %+v", events)
	}
}

func TestPruneMessagesKeepsNewest(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "conversations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := st.Create("prune", "", "", "/tmp/chat", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := st.AppendMessage(c.ID, "user", "m"+string(rune('0'+i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PruneMessages(c.ID, 2); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.Messages(c.ID, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Text != "m3" || msgs[1].Text != "m4" {
		t.Fatalf("messages = %+v", msgs)
	}
}
