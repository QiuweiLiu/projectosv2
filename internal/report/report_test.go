package report_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/report"
	"projectos/kernel/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func seedObjective(t *testing.T, st *store.Store, status goal.Status) goal.Objective {
	t.Helper()
	now := time.Now().UTC()
	obj := goal.Objective{ID: "obj-1", Goal: "写一份汇总", Status: status, PlanVersion: 1, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateObjective(obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestCompletedObjectiveReadsAsPlainChinese(t *testing.T) {
	st := openStore(t)
	obj := seedObjective(t, st, goal.StatusCompleted)
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "v", ArtifactPath: "out/summary.md"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Claim("T1", "r1", "fake"); !ok {
		t.Fatal("claim")
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunSucceeded, Accepted: true, Result: "x"}); err != nil {
		t.Fatal(err)
	}

	brief, err := report.Build(st, obj)
	if err != nil {
		t.Fatal(err)
	}
	text := report.Render(brief)
	for _, want := range []string{"已完成", "1 个已通过", "T1（summary.md）已通过", "下一步"} {
		if !strings.Contains(text, want) {
			t.Fatalf("brief is missing %q:\n%s", want, text)
		}
	}
}

func TestBlockedReasonIsTranslated(t *testing.T) {
	st := openStore(t)
	obj := seedObjective(t, st, goal.StatusFailed)
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "v", ArtifactPath: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Claim("T1", "r1", "fake"); !ok {
		t.Fatal("claim")
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunSucceeded,
		Accepted: false, Note: "review: blocking finding (P0/P1)"}); err != nil {
		t.Fatal(err)
	}

	brief, err := report.Build(st, obj)
	if err != nil {
		t.Fatal(err)
	}
	text := report.Render(brief)
	if strings.Contains(text, "blocking finding") {
		t.Fatalf("raw machine reason leaked into the briefing:\n%s", text)
	}
	if !strings.Contains(text, "审查发现了需要修的问题") {
		t.Fatalf("blocked reason was not translated:\n%s", text)
	}
}

func TestExplanationCacheIsBoundToFindings(t *testing.T) {
	dir := t.TempDir()
	findings := []domain.Finding{{Severity: domain.SeverityP2, Code: "naive", Message: "splits on commas"}}
	hash := report.FindingsHash(findings)
	if err := report.WriteExplanation(dir, "T1", hash, "中文解释"); err != nil {
		t.Fatal(err)
	}
	text, ok := report.ReadExplanation(dir, "T1", hash)
	if !ok || text != "中文解释" {
		t.Fatalf("cached text = %q, ok=%v", text, ok)
	}

	// New findings must invalidate the old explanation instead of showing stale text.
	changed := append(findings, domain.Finding{Severity: domain.SeverityP1, Code: "blank", Message: "skips blanks"})
	if _, ok := report.ReadExplanation(dir, "T1", report.FindingsHash(changed)); ok {
		t.Fatal("stale explanation was returned for different findings")
	}
}
