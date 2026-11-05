package store_test

import (
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/store"
)

func TestSetExternalVisible(t *testing.T) {
	s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()
	_ = s.CreateTask(domain.Task{ID: "A", Validator: "v"})
	r, ok, err := s.Claim("A", "run-1", "fake")
	t.Logf("claim=%v ok=%v err=%v", r, ok, err)
	if err := s.SetExternal(r.ID, "ext-1"); err != nil {
		t.Fatal(err)
	}
	runs, _ := s.ActiveRuns()
	for _, x := range runs {
		t.Logf("run id=%s ext=%q", x.ID, x.ExternalID)
	}
	if len(runs) != 1 || runs[0].ExternalID != "ext-1" {
		t.Fatal("external id not visible")
	}
}
