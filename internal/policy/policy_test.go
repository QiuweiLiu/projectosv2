package policy_test

import (
	"testing"

	"projectos/kernel/internal/policy"
)

func TestPacksAreDistinctAndNamed(t *testing.T) {
	simple, err := policy.Get("simple")
	if err != nil {
		t.Fatal(err)
	}
	research, err := policy.Get("research")
	if err != nil {
		t.Fatal(err)
	}
	if simple.RequireReview || !research.RequireReview {
		t.Fatalf("review requirement must differ: %+v %+v", simple, research)
	}
	if research.MaxAttempts < simple.MaxAttempts {
		t.Fatalf("research should allow more attempts")
	}
	def, err := policy.Get("")
	if err != nil || def.Name != policy.Default {
		t.Fatalf("default pack = %+v, %v", def, err)
	}
	if _, err := policy.Get("nope"); err == nil {
		t.Fatal("unknown pack must be rejected")
	}
}
