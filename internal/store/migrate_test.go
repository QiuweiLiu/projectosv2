package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/store"
	"projectos/kernel/internal/verify"
)

func TestOpenMigratesOldRunsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// A runs table from before the usage columns existed.
	_, err = raw.Exec(`CREATE TABLE runs (
		id TEXT PRIMARY KEY, task_id TEXT, attempt INTEGER, backend TEXT, status TEXT,
		result TEXT, evidence TEXT, external_id TEXT, started_at TEXT, finished_at TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	raw.Close()

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open should migrate: %v", err)
	}
	defer st.Close()
	if _, _, err := st.UsageChars(); err != nil {
		t.Fatalf("usage after migration: %v", err)
	}
}

func TestReadOnlyToleratesOldRunsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE runs (
		id TEXT PRIMARY KEY, task_id TEXT, attempt INTEGER, backend TEXT, status TEXT,
		result TEXT, evidence TEXT, external_id TEXT, started_at TEXT, finished_at TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	raw.Close()

	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, _, err := ro.UsageChars(); err != nil {
		t.Fatalf("read-only usage must tolerate the old schema: %v", err)
	}
}

func TestReadOnlyOldReviewsTableHasNoUsageColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// runs already migrated, reviews still old
	_, err = raw.Exec(`CREATE TABLE runs (
		id TEXT PRIMARY KEY, task_id TEXT, attempt INTEGER, backend TEXT, status TEXT,
		result TEXT, evidence TEXT, external_id TEXT, started_at TEXT, finished_at TEXT,
		prompt_chars INTEGER NOT NULL DEFAULT 0, output_chars INTEGER NOT NULL DEFAULT 0)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE reviews (
		id TEXT PRIMARY KEY, task_id TEXT, artifact_version TEXT, kind TEXT,
		findings TEXT, blocking INTEGER, at TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	raw.Close()

	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, _, err := ro.UsageChars(); err != nil {
		t.Fatalf("old reviews table must degrade to zero: %v", err)
	}
}

func TestReadOnlyWithoutMetaTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE tasks (id TEXT PRIMARY KEY, objective TEXT NOT NULL DEFAULT '',
		prompt TEXT NOT NULL DEFAULT '', validator TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'pending',
		attempts INTEGER NOT NULL DEFAULT 0, max_attempts INTEGER NOT NULL DEFAULT 1, last_error TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	value, err := ro.GetMeta("objective_hash")
	if err != nil || value != "" {
		t.Fatalf("missing meta table must read as empty: %q %v", value, err)
	}
}

func TestOpenMigratesOldPlansTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE plans (
		id TEXT PRIMARY KEY, objective_id TEXT, version INTEGER, status TEXT,
		spec TEXT, hash TEXT, created_at TEXT, confirmed_at TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	raw.Close()

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open should migrate plans: %v", err)
	}
	defer st.Close()
	if _, err := st.CreatePlanWithUsage("obj-1", 1, goal.Plan{Tasks: []goal.PlanTask{{
		ID: "T1", Goal: "g", Artifacts: []string{"a.txt"}, Risk: "low",
		Validators: []verify.Spec{{Type: "nonempty"}},
	}}}, "h", store.PlanUsage{PromptTokens: 1}); err != nil {
		t.Fatalf("create plan after migration: %v", err)
	}
}
