package store_test

import (
	"errors"
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/store"
)

func TestSecondControllerIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kernel.lock")
	first, err := store.AcquireLock(path)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer first.Release()

	if _, err := store.AcquireLock(path); !errors.Is(err, store.ErrLocked) {
		t.Fatalf("second lock err = %v, want ErrLocked", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	third, err := store.AcquireLock(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	third.Release()
}

// Opening for writing must take ownership of the state dir; otherwise a second
// controller could run recovery underneath a live scheduler.
func TestOpenOwnsStateDir(t *testing.T) {
	dir := t.TempDir()
	first, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := store.Open(filepath.Join(dir, "kernel.db")); !errors.Is(err, store.ErrLocked) {
		t.Fatalf("second open err = %v, want ErrLocked", err)
	}
	// Reading is allowed while a writer owns the dir.
	reader, err := store.OpenReadOnly(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatalf("read-only open: %v", err)
	}
	reader.Close()
	first.Close()

	again, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatalf("open after close: %v", err)
	}
	again.Close()
}

// A read must never rewrite live state. Recovery rewrites every running Run to
// unknown, so it belongs to the writer path only.
func TestReadOnlyOpenDoesNotRecover(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(domain.Task{ID: "A", Validator: "nonempty"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.Claim("A", "run-1", "fake"); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	st.Close()

	reader, err := store.OpenReadOnly(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	task, ok, err := reader.Get("A")
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if task.Status != domain.TaskRunning {
		t.Fatalf("read-only open changed status to %s", task.Status)
	}
	reader.Close()

	writer, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	recovered, _, err := writer.Get("A")
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != domain.TaskBlocked {
		t.Fatalf("writer open should recover to blocked, got %s", recovered.Status)
	}
}
