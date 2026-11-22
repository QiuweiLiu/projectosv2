package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceWarning(t *testing.T) {
	if got := workspaceWarning(string(filepath.Separator)); got != "root" {
		t.Fatalf("root = %q", got)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got := workspaceWarning(home); got != "home" {
		t.Fatalf("home = %q", got)
	}
	icloud := filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs", "proj")
	if got := workspaceWarning(icloud); got != "icloud" {
		t.Fatalf("icloud = %q", got)
	}
	if got := workspaceWarning(filepath.Join(home, "code", "proj")); got != "" {
		t.Fatalf("normal = %q", got)
	}
}
