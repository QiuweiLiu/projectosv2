package chat

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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
	ch, _, _, cancel, err := m.Subscribe(c.ID, 0)
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

func TestSubscribePaginatesEvents(t *testing.T) {
	m, st, c := testManager(t)
	for i := 0; i < 650; i++ {
		if _, err := st.AppendEvent(c.ID, "tool", map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	ch, _, gap, cancel, err := m.Subscribe(c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if gap {
		t.Fatal("fresh subscription reported a gap")
	}
	last := int64(0)
	for i := 0; i < 650; i++ {
		select {
		case ev := <-ch:
			if ev.Seq <= last {
				t.Fatalf("out of order: %d after %d", ev.Seq, last)
			}
			last = ev.Seq
		case <-time.After(time.Second):
			t.Fatalf("missing replay event %d", i)
		}
	}
}

func TestSubscribeReportsPrunedGap(t *testing.T) {
	m, st, c := testManager(t)
	for i := 0; i < 10; i++ {
		if _, err := st.AppendEvent(c.ID, "tool", map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PruneEvents(3); err != nil {
		t.Fatal(err)
	}
	ch, _, gap, cancel, err := m.Subscribe(c.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if !gap {
		t.Fatal("pruned cursor did not report a gap")
	}
	// The remaining events are still replayable and ordered.
	select {
	case ev := <-ch:
		if ev.Seq == 0 {
			t.Fatal("empty replay event")
		}
	case <-time.After(time.Second):
		t.Fatal("no post-prune replay")
	}
}

func TestSlowConsumerGetsResyncSignal(t *testing.T) {
	m, _, c := testManager(t)
	ch, done, _, cancel, err := m.Subscribe(c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	// Do not read ch; fill it past capacity.
	for i := 0; i < 5000; i++ {
		m.publish(conversation.Event{ConversationID: c.ID, Kind: "tool", Data: json.RawMessage(`{}`)})
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow consumer did not receive a resync signal")
	}
	_ = ch
}

func TestPromptWithContextAddsBoundedEvidence(t *testing.T) {
	first := promptWithContext("do it", "Verified: hello.txt")
	if !strings.Contains(first, "hello.txt") || !strings.Contains(first, "User request") {
		t.Fatalf("first = %q", first)
	}
	if got := promptWithContext("next", ""); got != "next" {
		t.Fatalf("empty context = %q", got)
	}
}

func TestParseHandoffDropsChatMetaInstructions(t *testing.T) {
	draft, err := parseHandoff(`{"goal":"创建 notes.txt；只回复收到，不要调用工具","constraints":["包含 hello","只回复收到","Do not use any other tools"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(draft.Goal, "只回复") || len(draft.Constraints) != 1 || draft.Constraints[0] != "包含 hello" {
		t.Fatalf("draft = %+v", draft)
	}
}

func TestCleanGoalRemovesEnglishChatMetaClause(t *testing.T) {
	goal := "Create notes.txt with hello. Do not use any other tools."
	if got := cleanGoal(goal); got != "Create notes.txt with hello." {
		t.Fatalf("goal = %q", got)
	}
	if got := cleanGoal("只回复收到"); got != "" {
		t.Fatalf("meta-only goal = %q", got)
	}
}

func TestContextRefreshOnlyWhenVersionChanges(t *testing.T) {
	c := conversation.Conversation{ProjectID: "p", ContextSent: true, ContextVersion: "v1"}
	if shouldInjectContext(c, "ctx", "v1") {
		t.Fatal("unchanged context must not be re-injected")
	}
	if !shouldInjectContext(c, "ctx", "v2") {
		t.Fatal("new evidence version must be injected")
	}
	if shouldInjectContext(conversation.Conversation{}, "", "") {
		t.Fatal("empty context must not be injected")
	}
}

func TestContextVersionIsQueryIndependent(t *testing.T) {
	m := &Manager{cfg: Config{ProjectContext: func(projectID, query string) string {
		if query == "" {
			return "stable project facts"
		}
		return "facts relevant to " + query
	}}}
	c := conversation.Conversation{ProjectID: "p"}
	text1, version1 := m.projectContext(c, "first question")
	text2, version2 := m.projectContext(c, "second question")
	if text1 == text2 {
		t.Fatal("query-specific context was not selected")
	}
	if version1 == "" || version1 != version2 {
		t.Fatalf("versions = %q %q", version1, version2)
	}
}

func TestReconcileStaleClearsRunningLeftByPreviousProcess(t *testing.T) {
	m, st, c := testManager(t)
	if err := st.SetStatus(c.ID, conversation.StatusRunning, ""); err != nil {
		t.Fatal(err)
	}
	if n := m.ReconcileStale(); n != 1 {
		t.Fatalf("reconciled = %d, want 1", n)
	}
	got, ok, err := st.Get(c.ID)
	if err != nil || !ok || got.Status != conversation.StatusInterrupted {
		t.Fatalf("conversation = %+v %v %v", got, ok, err)
	}
	if n := m.ReconcileStale(); n != 0 {
		t.Fatalf("second pass reconciled = %d, want 0", n)
	}
	events, err := st.EventsAfter(c.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		if ev.Kind == "status" && strings.Contains(string(ev.Data), "kernel restarted") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing status event: %+v", events)
	}
}

func TestRewindWithoutRemoteIDFallsBackToLocalOnly(t *testing.T) {
	m, st, c := testManager(t)
	user, err := st.AppendMessage(c.ID, "user", "撤回到这里")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(c.ID, "assistant", "之后的回答"); err != nil {
		t.Fatal(err)
	}
	res, err := m.Rewind(context.Background(), c.ID, user.ID, false)
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if res.RestoredText != "撤回到这里" || res.Removed != 2 {
		t.Fatalf("result = %+v", res)
	}
	if res.RemoteForked {
		t.Fatalf("a message without a remote id must not be forked: %+v", res)
	}
	if res.Session != "ses_1" {
		t.Fatalf("session changed to %q", res.Session)
	}
	msgs, err := st.Messages(c.ID, 10, "")
	if err != nil || len(msgs) != 0 {
		t.Fatalf("messages = %+v %v", msgs, err)
	}
	got, _, _ := st.Get(c.ID)
	if got.Status != conversation.StatusIdle {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestMatchServerUserMessageHandlesContextPrefixAndWrongID(t *testing.T) {
	at := time.Date(2026, 11, 10, 16, 9, 47, 0, time.UTC)
	msgs := []opencodeserver.Message{
		{ID: "msg_server_hello", Type: "user",
			Text: "Project context (verified facts and evidence):\n..."},
		{ID: "msg_server_assistant", Type: "assistant", Text: "你好！"},
		{ID: "msg_server_next", Type: "user", Text: "你能做什么"},
	}
	msgs[0].Text += "\n\n你好"
	msgs[0].Time.Created = at.Add(3 * time.Second).UnixMilli()
	msgs[1].Time.Created = at.Add(4 * time.Second).UnixMilli()
	msgs[2].Time.Created = at.Add(90 * time.Second).UnixMilli()
	msgs[2].Text = "你能做什么"

	if got := matchServerUserMessage(msgs, conversation.Message{
		Role: "user", Text: "你好", CreatedAt: at,
	}); got != "msg_server_hello" {
		t.Fatalf("hello matched %q", got)
	}
	if got := matchServerUserMessage(msgs, conversation.Message{
		Role: "user", Text: "你能做什么", CreatedAt: at.Add(88 * time.Second),
	}); got != "msg_server_next" {
		t.Fatalf("next matched %q", got)
	}
	if got := matchServerUserMessage(msgs, conversation.Message{
		Role: "user", Text: "不存在的消息", CreatedAt: at,
	}); got != "" {
		t.Fatalf("unknown matched %q", got)
	}
	// A short string in the middle of a different message is not a match.
	if got := matchServerUserMessage([]opencodeserver.Message{{
		ID: "msg_mid", Type: "user", Text: "please explain 你好 in detail",
		Time: msgs[0].Time,
	}}, conversation.Message{Role: "user", Text: "你好", CreatedAt: at}); got != "" {
		t.Fatalf("mid-string matched %q", got)
	}
}

func TestDetachObjectiveClearsConversationLink(t *testing.T) {
	m, st, c := testManager(t)
	if err := st.SetObjective(c.ID, "obj-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.DetachObjective(c.ID); err != nil {
		t.Fatalf("detach: %v", err)
	}
	got, ok, err := st.Get(c.ID)
	if err != nil || !ok || got.ObjectiveID != "" {
		t.Fatalf("conversation = %+v %v %v", got, ok, err)
	}
	events, err := st.EventsAfter(c.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		if ev.Kind == "objective_detached" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing objective_detached event: %+v", events)
	}
}

func TestAutoApproveReadOnlyActions(t *testing.T) {
	for _, action := range []string{"read", "list", "grep", "glob", "READ"} {
		if !autoApproveReadOnly(action) {
			t.Fatalf("%q should be auto-approved", action)
		}
	}
	for _, action := range []string{"edit", "bash", "shell", "webfetch", "WEBFETCH", "question", ""} {
		if autoApproveReadOnly(action) {
			t.Fatalf("%q must still ask", action)
		}
	}
}

func TestLocalOnlyRewindClearsOldSessionForRemoteMessage(t *testing.T) {
	m, st, c := testManager(t)
	user, err := st.AppendMessage(c.ID, "user", "remote turn")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetRemoteID(user.ID, "msg_remote_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(c.ID, "assistant", "answer"); err != nil {
		t.Fatal(err)
	}
	res, err := m.Rewind(context.Background(), c.ID, user.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.RemoteForked {
		t.Fatalf("localOnly forked: %+v", res)
	}
	got, _, _ := st.Get(c.ID)
	if got.SessionID != "" {
		t.Fatalf("old session survived local-only rewind: %q", got.SessionID)
	}
}

func TestDeterministicRiskRejectsObviousHighRisk(t *testing.T) {
	for _, tc := range []struct {
		action    string
		resources []string
	}{
		{"webfetch", []string{"http://127.0.0.1:8799/api/status"}},
		{"bash", []string{"rm -rf /tmp/x"}},
		{"shell", []string{"git reset --hard HEAD~1"}},
		{"edit", []string{"/etc/passwd"}},
		{"edit", []string{"../outside.txt"}},
	} {
		if reason, high := deterministicRisk(tc.action, tc.resources); !high || reason == "" {
			t.Fatalf("%s %v was not high risk", tc.action, tc.resources)
		}
	}
	for _, tc := range []struct {
		action    string
		resources []string
	}{
		{"bash", []string{"python3 -m pytest"}},
		{"edit", []string{"internal/app/main.go"}},
	} {
		if _, high := deterministicRisk(tc.action, tc.resources); high {
			t.Fatalf("%s %v was marked high risk", tc.action, tc.resources)
		}
	}
}
