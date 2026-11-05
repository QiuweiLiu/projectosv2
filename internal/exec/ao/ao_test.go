package ao

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/exec"
)

func TestStartSendsIdempotencyKeyAndParsesSession(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/sessions" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = w.Write([]byte(`{"session":{"id":"repo-7"}}`))
	}))
	defer srv.Close()

	b := New(Config{BaseURL: srv.URL, ProjectID: "repo", Model: "deepseek/deepseek-flash"})
	res, err := b.Start(context.Background(), exec.StartRequest{TaskID: "A", Attempt: 1, Prompt: "make a.txt"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if res.ExternalID != "repo-7" {
		t.Fatalf("external id = %q", res.ExternalID)
	}
	if got["clientRequestId"] != "kernel-A-1" {
		t.Fatalf("clientRequestId = %v", got["clientRequestId"])
	}
	if got["harness"] != "opencode-v2" || got["mode"] != "chat" {
		t.Fatalf("payload = %v", got)
	}
}

func TestObserveMapsTurnStates(t *testing.T) {
	cases := []struct {
		name string
		body string
		want domain.RunStatus
	}{
		{"busy", `{"controller":"busy","turns":[{"id":"t1","state":"running"}]}`, domain.RunRunning},
		{"completed", `{"controller":"ready","turns":[{"id":"t1","state":"completed"}]}`, domain.RunSucceeded},
		{"interrupted", `{"controller":"ready","turns":[{"id":"t1","state":"interrupted"}]}`, domain.RunCancelled},
		{"no turns yet", `{"controller":"ready","turns":[]}`, domain.RunRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			b := New(Config{BaseURL: srv.URL})
			state, err := b.Observe(context.Background(), "repo-1")
			if err != nil {
				t.Fatalf("observe: %v", err)
			}
			if state.Status != tc.want {
				t.Fatalf("status = %s, want %s", state.Status, tc.want)
			}
		})
	}
}

func TestCancelTreatsNoActiveTurnAsSettled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"conflict","code":"CHAT_NO_ACTIVE_TURN","message":"no turn"}`))
	}))
	defer srv.Close()
	b := New(Config{BaseURL: srv.URL})
	if err := b.Cancel(context.Background(), "repo-1"); err != nil {
		t.Fatalf("cancel should tolerate an already finished turn: %v", err)
	}
}

func TestCollectReadsArtifactNotAgentClaim(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/workspace/files"):
			_, _ = w.Write([]byte(`{"workspaceVersion":"v1"}`))
		case strings.HasSuffix(r.URL.Path, "/workspace/file"):
			if r.URL.Query().Get("path") != "a.txt" {
				t.Errorf("path query = %q", r.URL.Query().Get("path"))
			}
			_, _ = w.Write([]byte(`{"content":"alpha-42\n","fileFingerprint":"fp-a"}`))
		case strings.HasSuffix(r.URL.Path, "/conversation"):
			_, _ = w.Write([]byte(`{"controller":"ready","turns":[{"id":"t1","state":"completed"}],"messages":[{"role":"assistant","text":"I wrote it"}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	b := New(Config{BaseURL: srv.URL})
	art, err := b.Collect(context.Background(), "repo-1", "a.txt")
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if strings.TrimSpace(art.Result) != "alpha-42" {
		t.Fatalf("artifact = %q", art.Result)
	}
	if art.Version != "fp-a" {
		t.Fatalf("version = %q", art.Version)
	}
}

func TestStartRetriesTransportFailureWithSameKey(t *testing.T) {
	var calls int
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		keys = append(keys, body["clientRequestId"].(string))
		calls++
		if calls == 1 {
			panic(http.ErrAbortHandler) // close the connection: no HTTP response
		}
		_, _ = w.Write([]byte(`{"session":{"id":"repo-9"}}`))
	}))
	defer srv.Close()

	b := New(Config{BaseURL: srv.URL, ProjectID: "repo"})
	res, err := b.Start(context.Background(), exec.StartRequest{TaskID: "A", Attempt: 1, Prompt: "p"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if res.ExternalID != "repo-9" || calls != 2 {
		t.Fatalf("external=%q calls=%d", res.ExternalID, calls)
	}
	if keys[0] != keys[1] {
		t.Fatalf("retry must reuse the idempotency key: %v", keys)
	}
}

func TestStartPassesApprovalMode(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = w.Write([]byte(`{"session":{"id":"repo-1"}}`))
	}))
	defer srv.Close()
	b := New(Config{BaseURL: srv.URL, ProjectID: "repo"})
	if _, err := b.Start(context.Background(), exec.StartRequest{TaskID: "A", Attempt: 1, ApprovalMode: "default"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got["approvalMode"] != "default" {
		t.Fatalf("approvalMode = %v", got["approvalMode"])
	}
}

func TestStartAgainstClosedDaemonFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // daemon gone

	b := New(Config{BaseURL: url, ProjectID: "repo", StartTimeout: 2 * time.Second})
	if _, err := b.Start(context.Background(), exec.StartRequest{TaskID: "A", Attempt: 1}); err == nil {
		t.Fatal("start against a closed daemon must fail")
	}
}

func TestMatchesChatHostNonASCII(t *testing.T) {
	cmd := "/x/ao chat-host repo-1 /Users/me/项目 数据/运行 /Users/me/项目 数据/运行/wt acp tok -- opencode acp"
	if !matchesChatHost(cmd, "repo-1", "/Users/me/项目 数据/运行") {
		t.Fatal("non-ASCII data dir must match")
	}
	if matchesChatHost(cmd, "repo-1", "/Users/me/项目 数据/运行2") {
		t.Fatal("prefix-only match must be refused")
	}
}
