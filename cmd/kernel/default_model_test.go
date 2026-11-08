package main

import (
	"testing"

	"projectos/kernel/internal/modelcfg"
)

// Every role must fall back to the same built-in model when neither a flag nor
// models.json names one, otherwise a default change silently misses a role.
func TestDefaultModelAppliesToEveryRole(t *testing.T) {
	dir := t.TempDir()
	for _, role := range modelcfg.Roles {
		if got := resolveRoleModel(dir, role, ""); got != defaultModel {
			t.Errorf("role %s: got %q, want %q", role, got, defaultModel)
		}
	}
	if got := defaultChatModel(dir); got != defaultModel {
		t.Errorf("chat: got %q, want %q", got, defaultModel)
	}
}
