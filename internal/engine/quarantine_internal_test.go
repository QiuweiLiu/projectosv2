package engine

import (
	"os"
	"path/filepath"
	"testing"

	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/store"
)

// A database write failure must not lose quarantine: the file fallback keeps it
// across a restart, which is the whole point of the flag.
func TestQuarantineFallsBackToFileWhenDBWriteFails(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, exec.NewFake(1), 1, nil).WithStateDir(dir)

	// Closing the database makes every SetMeta fail, which is the injected fault.
	st.Close()
	e.setQuarantine("cancel could not be confirmed")

	flag := filepath.Join(dir, "QUARANTINE")
	raw, err := os.ReadFile(flag)
	if err != nil {
		t.Fatalf("quarantine file was not written: %v", err)
	}
	if string(raw) != "cancel could not be confirmed" {
		t.Fatalf("flag = %q", raw)
	}

	// A fresh process reopens the same dir and must still see the quarantine.
	st2, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	e2 := New(st2, exec.NewFake(1), 1, nil).WithStateDir(dir)
	if reason := e2.RestoreQuarantine(); reason != "cancel could not be confirmed" {
		t.Fatalf("restored %q", reason)
	}
	if err := e2.ClearQuarantine(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(flag); !os.IsNotExist(err) {
		t.Fatal("clear did not remove the quarantine file")
	}
}
