package opencodecli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseOutputFromRealLogShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	raw := `{"type":"step_start","sessionID":"ses_x","part":{"type":"step-start"}}
{"type":"step_finish","sessionID":"ses_x","part":{},"tokens":{"input":3982,"output":55,"reasoning":7}}
{"type":"step_finish","sessionID":"ses_x","part":{},"tokens":{"input":94,"output":50}}
{"type":"step_finish","sessionID":"ses_x","part":{},"tokens":{"input":227,"output":23}}
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	_, u := parseOutput(path)
	if u.input != 4303 || u.output != 128 || u.session != "ses_x" {
		t.Fatalf("usage = %+v", u)
	}
}
