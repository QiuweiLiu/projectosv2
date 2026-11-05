package main

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/objective"
	"projectos/kernel/internal/store"
)

func newEmptyStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateTask(domain.Task{ID: "A", Validator: "v"}); err != nil {
		t.Fatal(err)
	}
	_ = st.SetMeta("objective_name", "demo")
	_ = st.SetMeta("policy_pack", "simple")
	return st
}

func TestStatusJSONShape(t *testing.T) {
	st := newStore(t)
	rec := httptest.NewRecorder()
	serveStatus(rec, st)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var view statusView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ObjectiveName != "demo" || view.Pack != "simple" || len(view.Tasks) != 1 {
		t.Fatalf("view = %+v", view)
	}
}

func TestDashboardServesClientApp(t *testing.T) {
	rec := httptest.NewRecorder()
	serveDashboard(rec)
	body := rec.Body.String()
	for _, want := range []string{"/api/status", "/api/tasks/", "/api/events",
		"/api/objectives", "/api/report", "/confirm", "/cancel", "汇报", "已通过"} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard is missing %q", want)
		}
	}
}

func TestServeTaskDetail(t *testing.T) {
	st := newStore(t)
	if _, ok, _ := st.Claim("A", "run-1", "fake"); !ok {
		t.Fatal("claim failed")
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "run-1", RunStatus: domain.RunSucceeded, Accepted: true,
		Evidence: domain.Evidence{ArtifactVersion: "v1"}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	serveTask(rec, st, t.TempDir(), "A")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var detail taskDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != "A" || len(detail.Evidence) != 1 || !detail.Evidence[0].Accepted {
		t.Fatalf("detail = %+v", detail)
	}

	missing := httptest.NewRecorder()
	serveTask(missing, st, t.TempDir(), "nope")
	if missing.Code != 404 {
		t.Fatalf("missing task status = %d", missing.Code)
	}
}

func TestServeEventsPage(t *testing.T) {
	st := newStore(t)
	for i := 0; i < 3; i++ {
		_ = st.AppendEvent("A", "started", "x")
	}
	rec := httptest.NewRecorder()
	serveEvents(rec, st, 0, 10, false)
	var page struct {
		Events []domain.Event `json:"events"`
		Next   int64          `json:"next"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("events = %d", len(page.Events))
	}
	if page.Next != page.Events[2].Seq {
		t.Fatalf("next = %d, last seq = %d", page.Next, page.Events[2].Seq)
	}

	// Resuming from the cursor must not repeat what the caller already has.
	again := httptest.NewRecorder()
	serveEvents(again, st, page.Next, 10, false)
	var tail struct {
		Events []domain.Event `json:"events"`
		Next   int64          `json:"next"`
	}
	if err := json.Unmarshal(again.Body.Bytes(), &tail); err != nil {
		t.Fatal(err)
	}
	if len(tail.Events) != 0 || tail.Next != page.Next {
		t.Fatalf("resume returned %d events, next=%d", len(tail.Events), tail.Next)
	}
}

func TestResolveRunState(t *testing.T) {
	obj := objective.Objective{Name: "demo", Tasks: []objective.TaskSpec{
		{ID: "A", Validator: objective.ValidatorSpec{Type: "nonempty"}},
	}}

	st := newStore(t) // has task A, no meta yet
	if _, err := resolveRunState(st, obj, "h1", "simple"); err == nil {
		t.Fatal("tasks without metadata must be refused")
	}
	_ = st.SetMeta("objective_hash", "h1")
	_ = st.SetMeta("objective_name", "demo")
	resume, err := resolveRunState(st, obj, "h1", "simple")
	if err != nil || !resume {
		t.Fatalf("same hash should resume: %v %v", resume, err)
	}
	if _, err := resolveRunState(st, obj, "h2", "simple"); err == nil {
		t.Fatal("different hash must be refused")
	}

	empty := newEmptyStore(t)
	if resume, err := resolveRunState(empty, obj, "h1", "simple"); err != nil || resume {
		t.Fatalf("fresh dir should start clean: %v %v", resume, err)
	}
}
