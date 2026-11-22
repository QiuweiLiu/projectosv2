package modelcfg

import "testing"

func TestSetVariantAndFallbacks(t *testing.T) {
	var s Settings
	var err error
	if s, err = s.SetVariant("worker", "high"); err != nil {
		t.Fatal(err)
	}
	if got := s.VariantFor("worker"); got != "high" {
		t.Fatalf("worker variant = %q", got)
	}
	if got := s.VariantFor("reviewer"); got != "high" {
		t.Fatalf("reviewer should inherit worker variant, got %q", got)
	}

	if s, err = s.SetVariant("reviewer", "max"); err != nil {
		t.Fatal(err)
	}
	if got := s.VariantFor("reviewer"); got != "max" {
		t.Fatalf("reviewer variant = %q", got)
	}
	s.Reviewer = "provider/reviewer"
	if got := s.VariantFor("reviewer"); got != "max" {
		t.Fatalf("explicit reviewer model should keep its own variant, got %q", got)
	}

	if s, err = s.SetVariant("worker", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Variants["worker"]; ok {
		t.Fatal("empty variant should clear the entry")
	}
	if _, err := s.SetVariant("worker", "hi gh"); err == nil {
		t.Fatal("expected validation error")
	}
	if _, err := s.SetVariant("nope", "high"); err == nil {
		t.Fatal("expected unknown-role error")
	}
}

func TestWithVariant(t *testing.T) {
	if got := WithVariant("provider/model", "high"); got != "provider/model#high" {
		t.Fatalf("got %q", got)
	}
	if got := WithVariant("provider/model#low", "high"); got != "provider/model#low" {
		t.Fatalf("existing variant must win, got %q", got)
	}
	if got := WithVariant("", "high"); got != "" {
		t.Fatalf("empty model stays empty, got %q", got)
	}
}

func TestSetVariantDoesNotMutateSharedMap(t *testing.T) {
	base := Settings{Variants: map[string]string{"worker": "high"}}
	next, err := base.SetVariant("reviewer", "max")
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Variants) != 1 || base.Variants["reviewer"] != "" {
		t.Fatalf("source map mutated: %+v", base.Variants)
	}
	if next.Variants["reviewer"] != "max" || next.Variants["worker"] != "high" {
		t.Fatalf("next map = %+v", next.Variants)
	}
}
