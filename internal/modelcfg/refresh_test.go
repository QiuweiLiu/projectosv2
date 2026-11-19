package modelcfg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func fakeBin(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-opencode")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRefreshUsesPlainModels(t *testing.T) {
	dir := t.TempDir()
	bin := fakeBin(t, "#!/bin/sh\nif [ \"$1\" = models ]; then printf 'p/a\\np/b\\n'; fi\n")
	models, err := Refresh(context.Background(), dir, bin)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "p/a" || models[1] != "p/b" {
		t.Fatalf("models = %v", models)
	}
	if _, err := os.Stat(filepath.Join(dir, "models-cache.json")); err != nil {
		t.Fatalf("cache not written: %v", err)
	}
}

func TestRefreshFallsBackToStandalone(t *testing.T) {
	dir := t.TempDir()
	bin := fakeBin(t, "#!/bin/sh\nif [ \"$1\" = --standalone ]; then printf 'p/a\\np/b\\n'; fi\nexit 0\n")
	models, err := Refresh(context.Background(), dir, bin)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %v", models)
	}
}

// A CLI that prints nothing must not erase a good cache.
func TestRefreshKeepsCacheWhenCLIReturnsNothing(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "models-cache.json")
	if err := os.WriteFile(cache, []byte(`{"models":["keep/me"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := fakeBin(t, "#!/bin/sh\nexit 0\n")
	if _, err := Refresh(context.Background(), dir, bin); err == nil {
		t.Fatal("expected an error for an empty model list")
	}
	raw, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"models":["keep/me"]}` {
		t.Fatalf("cache was overwritten: %s", raw)
	}
}
