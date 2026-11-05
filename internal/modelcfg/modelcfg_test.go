package modelcfg_test

import (
	"testing"

	"projectos/kernel/internal/modelcfg"
)

func TestSettingsRoundTripAndValidation(t *testing.T) {
	dir := t.TempDir()
	s := modelcfg.Settings{}
	var err error
	if s, err = s.Set("worker", "opencode-go/deepseek-v4.1-flash"); err != nil {
		t.Fatal(err)
	}
	if s, err = s.Set("reviewer", "opencode-go/kimi-k3.1-pro"); err != nil {
		t.Fatal(err)
	}
	if err := modelcfg.Save(dir, s); err != nil {
		t.Fatal(err)
	}
	got := modelcfg.Load(dir)
	if got.Worker != s.Worker || got.Reviewer != s.Reviewer {
		t.Fatalf("loaded %+v, want %+v", got, s)
	}

	if _, err := s.Set("bogus", "x"); err == nil {
		t.Fatal("unknown role was accepted")
	}
	if _, err := s.Set("worker", "bad model; rm -rf"); err == nil {
		t.Fatal("invalid model was accepted")
	}
	if s, err = s.Set("worker", ""); err != nil || s.Worker != "" {
		t.Fatalf("clearing a role failed: %+v %v", s, err)
	}
}
