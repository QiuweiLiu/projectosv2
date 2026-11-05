package store_test

import (
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/store"
)

// Public entry points must reject empty identifiers instead of writing rows that
// can never be addressed again.
func TestStoreRejectsEmptyIdentifiers(t *testing.T) {
	if _, err := store.Open(""); err == nil {
		t.Fatal("Open(\"\") must not create an ephemeral database")
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateTask(domain.Task{ID: "", Validator: "v"}); err == nil {
		t.Fatal("CreateTask with an empty id must fail")
	}
	if err := s.CreateTask(domain.Task{ID: "A", Validator: ""}); err == nil {
		t.Fatal("CreateTask without a validator must fail")
	}
	if err := s.CreateTask(domain.Task{ID: "A", Validator: "v"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Claim("", "run-1", "fake"); err == nil {
		t.Fatal("Claim with an empty task id must fail")
	}
	if _, _, err := s.Claim("A", "", "fake"); err == nil {
		t.Fatal("Claim with an empty run id must fail")
	}
	if _, err := s.SettleRun(store.Settlement{RunID: ""}); err == nil {
		t.Fatal("SettleRun with an empty run id must fail")
	}
	if err := s.AddReview(domain.Review{TaskID: "", ArtifactVersion: "v1", Kind: domain.ReviewFull}); err == nil {
		t.Fatal("AddReview with an empty task id must fail")
	}
	if err := s.SetMeta("", "x"); err == nil {
		t.Fatal("SetMeta with an empty key must fail")
	}
}
