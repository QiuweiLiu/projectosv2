package store_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/store"
)

func TestOpenReadOnlyDoesNotRecover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	w, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.CreateTask(domain.Task{ID: "A", Validator: "v"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := w.Claim("A", "run-1", "fake"); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	w.Close()

	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	task, _, _ := ro.Get("A")
	if task.Status != domain.TaskRunning {
		t.Fatalf("read-only open must not mark running tasks: got %s", task.Status)
	}
	if err := ro.CreateTask(domain.Task{ID: "B", Validator: "v"}); err == nil {
		t.Fatal("read-only store must reject writes")
	}
}

func TestLateResultIsRejectedAsStale(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
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
	if _, err := s.SettleRun(store.Settlement{RunID: "run-1", RunStatus: domain.RunSucceeded, Accepted: true}); err != nil {
		t.Fatalf("first settle: %v", err)
	}
	if _, err := s.SettleRun(store.Settlement{RunID: "run-1", RunStatus: domain.RunSucceeded, Accepted: true}); !errors.Is(err, store.ErrStaleRun) {
		t.Fatalf("second settle err = %v, want ErrStaleRun", err)
	}
}

func TestActiveRunsRejectsBadTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(domain.Task{ID: "A", Validator: "v"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Claim("A", "run-1", "fake"); !ok {
		t.Fatal("claim failed")
	}
	s.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE runs SET started_at='not-a-time' WHERE id='run-1'`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.ActiveRuns(); err == nil {
		t.Fatal("a bad timestamp must be an error, not a zero time")
	}
}
