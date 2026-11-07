package conversation

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
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

func TestRemoteIDsPreserveIdenticalTextAndAreIdempotent(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "conversations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := st.Create("remote", "", "", "/tmp/chat", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessageWithRemote(c.ID, "user", "same question", "msg-a", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessageWithRemote(c.ID, "user", "same question", "msg-b", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessageWithRemote(c.ID, "user", "same question", "msg-a", time.Now()); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.Messages(c.ID, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v", msgs)
	}
	if msgs[0].RemoteID == "" || msgs[1].RemoteID == "" || msgs[0].RemoteID == msgs[1].RemoteID {
		t.Fatalf("remote ids = %+v", msgs)
	}
}

func TestMessagesMigrationAddsRemoteID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE messages (
		id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, role TEXT NOT NULL,
		text TEXT NOT NULL, created_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO messages (id,conversation_id,role,text,created_at)
		VALUES ('m1','c1','user','old','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.Messages("c1", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].RemoteID != "" {
		t.Fatalf("migrated = %+v", msgs)
	}
}

func TestGoalDraftLifecycle(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "conversations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := st.Create("handoff", "proj-1", "", "/tmp/chat", "")
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.CreateGoalDraft(GoalDraft{ConversationID: c.ID, ProjectID: "proj-1",
		Goal: "build a todo app", Constraints: []string{"no database"},
		Acceptance: []string{"tests pass"}, SourceMessageIDs: []string{"m1", "m2"}, ContextVersion: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == "" || d.Status != "draft" {
		t.Fatalf("draft = %+v", d)
	}
	d.Goal = "build a Python todo app"
	if err := st.UpdateGoalDraft(d); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.LatestGoalDraft(c.ID)
	if err != nil || !ok || got.Goal != "build a Python todo app" || len(got.Constraints) != 1 {
		t.Fatalf("latest = %+v %v %v", got, ok, err)
	}
	if err := st.ConfirmGoalDraft(d.ID, "obj-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateGoalDraft(d); err == nil {
		t.Fatal("confirmed draft remained editable")
	}
	conv, ok, err := st.Get(c.ID)
	if err != nil || !ok || conv.ObjectiveID != "obj-1" {
		t.Fatalf("conversation = %+v %v %v", conv, ok, err)
	}
}

func TestPermissionEventDeduplication(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "conversations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := st.Create("perm", "", "", "/tmp/chat", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendEvent(c.ID, "permission", map[string]string{"requestId": "per_123"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.HasPermissionEvent(c.ID, "per_123"); err != nil || !ok {
		t.Fatalf("seen = %v %v", ok, err)
	}
	if ok, err := st.HasPermissionEvent(c.ID, "per_other"); err != nil || ok {
		t.Fatalf("other = %v %v", ok, err)
	}
}

func TestContextVersionPersistsAndRefreshes(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "conversations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := st.Create("ctx", "proj-1", "", "/tmp/chat", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkContextSent(c.ID, "hash-1"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.Get(c.ID)
	if err != nil || !ok || !got.ContextSent || got.ContextVersion != "hash-1" {
		t.Fatalf("conversation = %+v %v %v", got, ok, err)
	}
	if err := st.MarkContextSent(c.ID, "hash-2"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = st.Get(c.ID)
	if got.ContextVersion != "hash-2" {
		t.Fatalf("context version = %q", got.ContextVersion)
	}
	list, err := st.List(10)
	if err != nil || len(list) != 1 || list[0].ContextVersion != "hash-2" {
		t.Fatalf("list = %+v %v", list, err)
	}
}

func TestLongConversationPagination(t *testing.T) {
	started := time.Now()
	dbPath := filepath.Join(t.TempDir(), "conversations.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := st.Create("long", "", "", "/tmp/chat", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1200; i++ {
		if _, err := st.AppendMessage(c.ID, "user", "message-"+itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	before := ""
	for page := 0; page < 20; page++ {
		msgs, err := st.Messages(c.ID, 100, before)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) == 0 {
			break
		}
		for _, msg := range msgs {
			if seen[msg.ID] {
				t.Fatalf("duplicate message on pagination: %s", msg.ID)
			}
			seen[msg.ID] = true
		}
		before = msgs[0].CreatedAt.Format(time.RFC3339Nano)
		if len(msgs) < 100 {
			break
		}
	}
	if len(seen) != 1200 {
		t.Fatalf("read %d of 1200 messages", len(seen))
	}
	if info, err := os.Stat(dbPath); err == nil {
		t.Logf("messages=1200 elapsed_ms=%d db_bytes=%d", time.Since(started).Milliseconds(), info.Size())
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
