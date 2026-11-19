package opencodeserver

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseModel(t *testing.T) {
	m, err := parseModel("opencode-go/deepseek-v4.1-flash")
	if err != nil || m["providerID"] != "opencode-go" || m["id"] != "deepseek-v4.1-flash" {
		t.Fatalf("model = %+v %v", m, err)
	}
	if _, err := parseModel("missing-slash"); err == nil {
		t.Fatal("invalid model accepted")
	}
	withVariant, err := parseModel("opencode-go/deepseek-v4.1-flash#high")
	if err != nil || withVariant["variant"] != "high" || withVariant["id"] != "deepseek-v4.1-flash" {
		t.Fatalf("variant model = %+v %v", withVariant, err)
	}
	if _, err := parseModel("opencode-go/deepseek-v4.1-flash#"); err == nil {
		t.Fatal("empty variant accepted")
	}
}

func TestEventStreamSkipsHeartbeatAndSplitsEvents(t *testing.T) {
	raw := ": heartbeat\n\n" +
		"data: {\"id\":\"e1\",\"type\":\"session.text.delta\",\"data\":{\"delta\":\"hi\"}}\n\n" +
		"data: {\"id\":\"e2\",\"type\":\"session.text.ended\",\"data\":{\"text\":\"hi\"}}\n\n"
	stream := &EventStream{scanner: bufio.NewScanner(strings.NewReader(raw))}
	ev, err := stream.Next()
	if err != nil || ev.Type != "session.text.delta" || string(ev.Data) != `{"delta":"hi"}` {
		t.Fatalf("event = %+v %v", ev, err)
	}
	ev, err = stream.Next()
	if err != nil || ev.Type != "session.text.ended" {
		t.Fatalf("second event = %+v %v", ev, err)
	}
}

func TestStartFailsWhenServerExitsBeforePassword(t *testing.T) {
	bin := t.TempDir() + "/failing-opencode.sh"
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := Start(ctx, Config{Bin: bin, StartupTimeout: 300 * time.Millisecond})
	if err == nil {
		t.Fatal("server exit was reported as successful startup")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("startup failure took too long: %s", time.Since(start))
	}
}

func TestReplyPermissionMapsGoneRequestToNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"name":"PermissionNotFoundError"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	c := &Server{baseURL: srv.URL, password: "test"}
	err := c.ReplyPermission(context.Background(), "ses_1", "per_1", "reject", "")
	if !errors.Is(err, ErrPermissionNotFound) {
		t.Fatalf("err = %v, want ErrPermissionNotFound", err)
	}
}

func TestReplyPermissionKeepsOtherFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := &Server{baseURL: srv.URL, password: "test"}
	err := c.ReplyPermission(context.Background(), "ses_1", "per_1", "reject", "")
	if err == nil || errors.Is(err, ErrPermissionNotFound) {
		t.Fatalf("err = %v", err)
	}
	var he *HTTPError
	if !errors.As(err, &he) || he.StatusCode != http.StatusInternalServerError {
		t.Fatalf("err = %v, want HTTPError 500", err)
	}
}

func TestForkSessionMapsMissingMessageToTypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"name":"MessageNotFoundError","message":"Message not found: msg_1"}`))
	}))
	defer srv.Close()
	c := &Server{baseURL: srv.URL, password: "test"}
	_, err := c.ForkSession(context.Background(), "ses_1", "msg_1")
	if !errors.Is(err, ErrForkMessageNotFound) {
		t.Fatalf("err = %v, want ErrForkMessageNotFound", err)
	}
}

func TestForkSessionKeepsOtherErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := &Server{baseURL: srv.URL, password: "test"}
	_, err := c.ForkSession(context.Background(), "ses_1", "msg_1")
	if err == nil || errors.Is(err, ErrForkMessageNotFound) {
		t.Fatalf("err = %v", err)
	}
}
