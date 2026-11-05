package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func makeWorkDir(t *testing.T, root, name string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(root, "work", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(dir, when, when); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPruneWorkKeepsNewest(t *testing.T) {
	root := t.TempDir()
	old := makeWorkDir(t, root, "T1-1-old", 3*time.Hour)
	mid := makeWorkDir(t, root, "T1-2-mid", 2*time.Hour)
	newest := makeWorkDir(t, root, "T1-3-new", time.Hour)

	removed, err := pruneWork(root, 2)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("oldest work dir was kept")
	}
	for _, dir := range []string{mid, newest} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("%s was pruned", dir)
		}
	}
}

func TestPruneWorkKeepsBusyDirectory(t *testing.T) {
	root := t.TempDir()
	busy := makeWorkDir(t, root, "T1-1-busy", 3*time.Hour)
	if err := os.WriteFile(filepath.Join(busy, ".kernel-run.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(busy, old, old); err != nil {
		t.Fatal(err)
	}
	newest := makeWorkDir(t, root, "T1-2-new", time.Hour)
	if !workDirBusy(busy) {
		t.Fatalf("test setup: busy dir not detected, pid file = %q", filepath.Join(busy, ".kernel-run.pid"))
	}
	removed, err := pruneWork(root, 1)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
	for _, dir := range []string{busy, newest} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("%s was pruned", dir)
		}
	}
}

func TestPruneWorkZeroKeepsEverything(t *testing.T) {
	root := t.TempDir()
	dir := makeWorkDir(t, root, "T1-1-old", 3*time.Hour)
	if removed, err := pruneWork(root, 0); err != nil || removed != 0 {
		t.Fatalf("prune(0) = %d, %v", removed, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("keep-all pruned a directory")
	}
}
