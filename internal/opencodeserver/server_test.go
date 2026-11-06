package opencodeserver

import (
	"bufio"
	"strings"
	"testing"
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
