package store_test

import (
	"errors"
	"path/filepath"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "reviews.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestFullReviewIsLimitedToOnePerVersion(t *testing.T) {
	s := newStore(t)
	rev := domain.Review{TaskID: "A", ArtifactVersion: "v1", Kind: domain.ReviewFull,
		Findings: []domain.Finding{{Severity: domain.SeverityP2, Code: "style"}}}
	if err := s.AddReview(rev); err != nil {
		t.Fatalf("first full review: %v", err)
	}
	if err := s.AddReview(rev); !errors.Is(err, store.ErrReviewLimit) {
		t.Fatalf("second full review err = %v, want ErrReviewLimit", err)
	}
}

func TestTargetedReviewNeedsFullAndIsLimited(t *testing.T) {
	s := newStore(t)
	targeted := domain.Review{TaskID: "A", ArtifactVersion: "v1", Kind: domain.ReviewTargeted,
		Findings: []domain.Finding{{Severity: domain.SeverityP1, Code: "bug"}}}
	if err := s.AddReview(targeted); err == nil {
		t.Fatal("targeted review without a full review must be refused")
	}
	full := domain.Review{TaskID: "A", ArtifactVersion: "v1", Kind: domain.ReviewFull,
		Findings: []domain.Finding{{Severity: domain.SeverityP1, Code: "bug"}}}
	if err := s.AddReview(full); err != nil {
		t.Fatalf("full review: %v", err)
	}
	if err := s.AddReview(targeted); err != nil {
		t.Fatalf("first targeted review: %v", err)
	}
	if err := s.AddReview(targeted); !errors.Is(err, store.ErrReviewLimit) {
		t.Fatalf("second targeted review err = %v, want ErrReviewLimit", err)
	}
}

func TestTargetedReviewRejectsUnrelatedFinding(t *testing.T) {
	s := newStore(t)
	_ = s.AddReview(domain.Review{TaskID: "A", ArtifactVersion: "v1", Kind: domain.ReviewFull,
		Findings: []domain.Finding{{Severity: domain.SeverityP1, Code: "bug"}}})
	err := s.AddReview(domain.Review{TaskID: "A", ArtifactVersion: "v1", Kind: domain.ReviewTargeted,
		Findings: []domain.Finding{{Severity: domain.SeverityP3, Code: "naming"}}})
	if !errors.Is(err, store.ErrReviewScope) {
		t.Fatalf("err = %v, want ErrReviewScope", err)
	}
}

func TestTargetedReviewAllowedOnNewVersion(t *testing.T) {
	s := newStore(t)
	_ = s.AddReview(domain.Review{TaskID: "A", ArtifactVersion: "v1", Kind: domain.ReviewFull,
		Findings: []domain.Finding{{Severity: domain.SeverityP1, Code: "bug"}}})
	err := s.AddReview(domain.Review{TaskID: "A", ArtifactVersion: "v2", Kind: domain.ReviewTargeted,
		Findings: []domain.Finding{{Severity: domain.SeverityP1, Code: "bug"}}})
	if err != nil {
		t.Fatalf("targeted review of the fixed version should be allowed: %v", err)
	}
}

func TestReviewOfStaleVersionIsRefused(t *testing.T) {
	s := newStore(t)
	err := s.AddReview(domain.Review{TaskID: "A", Kind: domain.ReviewFull})
	if !errors.Is(err, store.ErrReviewStale) {
		t.Fatalf("err = %v, want ErrReviewStale", err)
	}
}

func TestBlockingFindingIsRecordedPerVersion(t *testing.T) {
	s := newStore(t)
	_ = s.AddReview(domain.Review{TaskID: "A", ArtifactVersion: "v1", Kind: domain.ReviewFull,
		Findings: []domain.Finding{{Severity: domain.SeverityP1, Code: "bug"}}})
	blocked, err := s.HasBlockingReview("A", "v1")
	if err != nil || !blocked {
		t.Fatalf("v1 blocking = %v, %v", blocked, err)
	}
	next, err := s.HasBlockingReview("A", "v2")
	if err != nil || next {
		t.Fatalf("v2 blocking = %v, %v; a new artifact version starts clean", next, err)
	}
}

func TestCleanTargetedReviewClearsEarlierBlocking(t *testing.T) {
	s := newStore(t)
	if err := s.AddReview(domain.Review{TaskID: "A", ArtifactVersion: "v1", Kind: domain.ReviewFull,
		Findings: []domain.Finding{{Severity: domain.SeverityP1, Code: "bug"}}}); err != nil {
		t.Fatal(err)
	}
	if blocked, _ := s.HasBlockingReview("A", "v1"); !blocked {
		t.Fatal("full review should block")
	}
	if err := s.AddReview(domain.Review{TaskID: "A", ArtifactVersion: "v1", Kind: domain.ReviewTargeted}); err != nil {
		t.Fatal(err)
	}
	blocked, err := s.HasBlockingReview("A", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Fatal("a clean targeted review must clear the earlier blocking full review")
	}
}
