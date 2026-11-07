package opencodeserver

import (
	"bufio"
	"context"
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
