package main

import "testing"

// Chat already honoured OPENCODE_BIN; every role backend must use the same
// binary or a v1 CLI on PATH can reject a v2 config and fail the objective.
func TestOpencodeBinHonoursEnvironment(t *testing.T) {
	t.Setenv("OPENCODE_BIN", " /opt/opencode-v2/opencode ")
	if got := opencodeBin(); got != "/opt/opencode-v2/opencode" {
		t.Fatalf("opencodeBin() = %q", got)
	}
	t.Setenv("OPENCODE_BIN", "")
	if got := opencodeBin(); got != "" {
		t.Fatalf("empty OPENCODE_BIN should fall back to PATH, got %q", got)
	}
}
