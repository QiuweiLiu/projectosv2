package ao

import (
	"context"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/exec"
)

func TestEmptyConfigFailsInsteadOfPanicking(t *testing.T) {
	b := New(Config{})
	if _, err := b.Start(context.Background(), exec.StartRequest{TaskID: "A", Attempt: 1}); err == nil {
		t.Fatal("a backend without a base URL must fail")
	}
	if _, err := b.Collect(context.Background(), "", ""); err == nil {
		t.Fatal("collect without a session must fail")
	}
}

func TestReapIsSafeWithEmptyArgs(t *testing.T) {
	if pids, err := Reap("", ""); err != nil || len(pids) != 0 {
		t.Fatalf("Reap empty = %v, %v", pids, err)
	}
	if pids, err := ReapAll(""); err != nil || len(pids) != 0 {
		t.Fatalf("ReapAll empty = %v, %v", pids, err)
	}
}

func TestNilReviewerFailsClosed(t *testing.T) {
	var r *Reviewer
	out, err := r.Review(context.Background(), exec.ReviewRequest{Task: domain.Task{ID: "A"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Findings) != 1 || out.Findings[0].Severity != domain.SeverityP1 {
		t.Fatalf("nil reviewer must fail closed: %#v", out.Findings)
	}
}
