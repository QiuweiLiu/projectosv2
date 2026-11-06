package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"projectos/kernel/internal/chat"
	"projectos/kernel/internal/conversation"
)

func TestChatAPIRequiresTokenAndDoesNotCreateTasks(t *testing.T) {
	root := t.TempDir()
	cstore, err := conversation.Open(filepath.Join(root, "conversations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cstore.Close()
	mgr := chat.New(chat.Config{Bin: "/nonexistent-opencode", Root: root, DefaultModel: "provider/model"}, cstore)
	defer mgr.Close()
	ws := newWorkspace(root)
	mux, ctl, err := buildMux(ws, true, mgr)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(mux)
	defer ts.Close()

	res, err := http.Get(ts.URL + "/api/conversations")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized list = %d", res.StatusCode)
	}
	res.Body.Close()

	body := strings.NewReader(`{"title":"chat","model":"provider/model"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/conversations", body)
	req.Header.Set("X-Kernel-Token", ctl.token)
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create = %d", res.StatusCode)
	}
	var c conversation.Conversation
	if err := json.NewDecoder(res.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	if c.ID == "" || c.Status != conversation.StatusIdle {
		t.Fatalf("conversation = %+v", c)
	}

	// A chat send must fail closed when the runtime is absent, and it must not
	// create a Kernel objective or task.
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/conversations/"+c.ID+"/messages",
		strings.NewReader(`{"text":"hello"}`))
	req.Header.Set("X-Kernel-Token", ctl.token)
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("send without runtime = %d", res.StatusCode)
	}
	res.Body.Close()

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/objectives", nil)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var rows []objectiveSummary
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("chat created objectives: %+v", rows)
	}
}

func TestSplitConversationPath(t *testing.T) {
	cases := []struct {
		path, id, action string
		ok               bool
	}{
		{"/api/conversations/conv-1", "conv-1", "", true},
		{"/api/conversations/conv-1/messages", "conv-1", "messages", true},
		{"/api/conversations/conv-1/events", "conv-1", "events", true},
		{"/api/conversations/", "", "", false},
	}
	for _, c := range cases {
		id, action, ok := splitConversationPath(c.path)
		if id != c.id || action != c.action || ok != c.ok {
			t.Fatalf("%s -> (%q,%q,%v), want (%q,%q,%v)", c.path, id, action, ok, c.id, c.action, c.ok)
		}
	}
}
