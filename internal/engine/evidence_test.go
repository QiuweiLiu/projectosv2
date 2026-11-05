package engine_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/engine"
	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/store"
)

func TestEvidencePinsAcceptedArtifact(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"], fake.Output["B"], fake.Output["C"] = "A-OK", "B-OK", "A-OK+B-OK"
	e, st := newEngine(t, fake, 2)
	diamond(t, e, 1)
	if _, err := e.Run(context.Background(), 50); err != nil {
		t.Fatalf("run: %v", err)
	}
	rows, err := st.EvidenceFor("A")
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if len(rows) != 1 || !rows[0].Accepted {
		t.Fatalf("expected one accepted evidence row, got %#v", rows)
	}
	sum := sha256.Sum256([]byte("A-OK"))
	if rows[0].Digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest = %q", rows[0].Digest)
	}
	if !strings.HasPrefix(rows[0].ArtifactVersion, "sha256:") {
		t.Fatalf("version = %q", rows[0].ArtifactVersion)
	}
}

func TestEvidenceRecordsRejectionReason(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"], fake.Output["B"] = "WRONG", "B-OK"
	e, st := newEngine(t, fake, 2)
	diamond(t, e, 1)
	if _, err := e.Run(context.Background(), 50); err != nil {
		t.Fatalf("run: %v", err)
	}
	rows, err := st.EvidenceFor("A")
	if err != nil || len(rows) == 0 {
		t.Fatalf("evidence: %v %v", rows, err)
	}
	last := rows[len(rows)-1]
	if last.Accepted || last.Reason == "" {
		t.Fatalf("expected a rejected row with a reason, got %#v", last)
	}
}

func TestBudgetStopsNewRuns(t *testing.T) {
	fake := exec.NewFake(1)
	fake.Output["A"], fake.Output["B"], fake.Output["C"] = "A-OK", "B-OK", "A-OK+B-OK"
	st, err := store.Open(filepath.Join(t.TempDir(), "budget.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := engine.New(st, fake, 2, validators()).WithBudget(engine.Budget{MaxRuns: 1})
	diamond(t, e, 1)
	if _, err := e.Run(context.Background(), 50); err != nil {
		t.Fatalf("run: %v", err)
	}
	runs, _ := st.EvidenceFor("A")
	tasks, _ := st.List()
	started := 0
	for _, task := range tasks {
		if task.Attempts > 0 {
			started++
		}
	}
	if started != 1 || len(runs) != 1 {
		t.Fatalf("expected exactly one run, started=%d evidence=%d", started, len(runs))
	}
	for _, id := range []domain.TaskID{"B", "C"} {
		task, _, _ := st.Get(id)
		if task.Status != domain.TaskBlocked {
			t.Fatalf("%s should be blocked by budget, got %s", id, task.Status)
		}
	}
}
