package diff

import (
	"strings"
	"testing"
)

func TestTextMarksAddsAndDeletes(t *testing.T) {
	lines, err := Text("a\nb\nc\n", "a\nB\nc\nd\n")
	if err != nil {
		t.Fatal(err)
	}
	got := Render(lines)
	want := " a\n-b\n+B\n c\n+d\n"
	if got != want {
		t.Fatalf("diff =\n%q\nwant\n%q", got, want)
	}
	if !Changed(lines) {
		t.Fatal("changed = false")
	}
}

func TestTextIdenticalIsEmpty(t *testing.T) {
	lines, err := Text("a\n", "a\n")
	if err != nil {
		t.Fatal(err)
	}
	if Changed(lines) {
		t.Fatalf("identical files reported as changed: %+v", lines)
	}
}

func TestTextRejectsHugeInput(t *testing.T) {
	if _, err := Text(strings.Repeat("x", MaxBytes+1), "x"); err == nil {
		t.Fatal("oversized input accepted")
	}
}

func TestIdenticalInputsShortCircuit(t *testing.T) {
	if lines, err := Text("same\n", "same\n"); err != nil || lines != nil {
		t.Fatalf("identical = %+v %v", lines, err)
	}
	big := strings.Repeat("x", MaxBytes+1)
	if lines, err := Text(big, big); err != nil || lines != nil {
		t.Fatalf("identical oversized = %+v %v", lines, err)
	}
}

func TestTrailingNewlineDoesNotExceedLineLimit(t *testing.T) {
	old := strings.Repeat("a\n", MaxLines)
	if _, err := Text(old, "a\n"); err != nil {
		t.Fatalf("a file with exactly the line limit was rejected: %v", err)
	}
}
