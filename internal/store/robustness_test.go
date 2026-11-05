package store_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/store"
)

// A state directory must survive paths that contain spaces, non-ASCII names and
// deep nesting; these are exactly the shapes that break naive path handling.
func TestStateDirPathMatrix(t *testing.T) {
	root := t.TempDir()
	deep := root
	for i := 0; i < 8; i++ {
		deep = filepath.Join(deep, strings.Repeat("dir", 4))
	}
	cases := map[string]string{
		"spaces":  filepath.Join(root, "with space", "state"),
		"chinese": filepath.Join(root, "项目 数据", "状态"),
		"deep":    filepath.Join(deep, "leaf"),
	}
	for name, dir := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "kernel.db")
			s, err := store.Open(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if err := s.CreateTask(domain.Task{ID: "A", Validator: "v", ArtifactPath: "结果 文件.txt"}); err != nil {
				t.Fatal(err)
			}
			run, ok, err := s.Claim("A", "run-1", "fake")
			if err != nil || !ok {
				t.Fatalf("claim: %v %v", ok, err)
			}
			if _, err := s.SettleRun(store.Settlement{RunID: run.ID, RunStatus: domain.RunSucceeded, Accepted: true}); err != nil {
				t.Fatal(err)
			}
			s.Close()

			ro, err := store.OpenReadOnly(path)
			if err != nil {
				t.Fatal(err)
			}
			defer ro.Close()
			task, _, err := ro.Get("A")
			if err != nil || task.Status != domain.TaskAccepted {
				t.Fatalf("reopen: %v %v", task.Status, err)
			}
		})
	}
}

func TestReadOnlyDirectoryFailsClearly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.MkdirAll(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	if _, err := store.Open(filepath.Join(dir, "kernel.db")); err == nil {
		t.Fatal("opening a store in a read-only directory must fail")
	}
}

func TestReadOnlyDatabaseRejectsWrites(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "kernel.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o644)
	if _, err := store.Open(path); err == nil {
		t.Fatal("a read-only database file must not silently open for writing")
	}
}

func TestFutureTimestampDoesNotLookExpired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateTask(domain.Task{ID: "A", Validator: "v"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Claim("A", "run-1", "fake"); !ok {
		t.Fatal("claim failed")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE runs SET started_at='2999-01-01T00:00:00Z' WHERE id='run-1'`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	runs, err := s.ActiveRuns()
	if err != nil || len(runs) != 1 {
		t.Fatalf("active runs: %d %v", len(runs), err)
	}
	if !runs[0].StartedAt.After(time.Now()) {
		t.Fatalf("future timestamp parsed as %s", runs[0].StartedAt)
	}
}

// A corrupt database must be reported, not replaced or silently recreated.
func TestOpenRejectsCorruptDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kernel.db")
	garbage := []byte("this is definitely not a sqlite database")
	if err := os.WriteFile(path, garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(path); err == nil {
		t.Fatal("corrupt database was accepted")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(garbage) {
		t.Fatal("open modified the corrupt file instead of failing")
	}
}
