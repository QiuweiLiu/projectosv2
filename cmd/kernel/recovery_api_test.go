package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/store"
)

func TestRecoveryAPIRefusesAndThenRequeuesWithApproval(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "projects", "obj-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeProjectMeta(dir, defaultProjectID); err != nil {
		t.Fatal(err)
	}
	st := seedGoal(t, dir)
	if _, ok, err := st.Claim("T2", "run-2", "fake"); err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "run-2", RunStatus: domain.RunSucceeded,
		Note: `verification: artifact does not contain "done"`}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	ws := newWorkspace(root)
	mux, ctl, err := buildMux(ws, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/objectives/obj-1/recover",
		strings.NewReader(`{"approve":false}`))
	req.Header.Set("X-Kernel-Token", ctl.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Decision struct {
			Action string `json:"action"`
			TaskID string `json:"taskId"`
		} `json:"decision"`
	}
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if view.Decision.Action != "retry" || view.Decision.TaskID != "T2" {
		t.Fatalf("decision = %+v", view)
	}

	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/objectives/obj-1/recover",
		strings.NewReader(`{"approve":true}`))
	req.Header.Set("X-Kernel-Token", ctl.token)
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("approve status = %d", res.StatusCode)
	}
	ro, err := store.OpenReadOnly(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	task, _, _ := ro.Get(domain.TaskID("T2"))
	if task.Status != domain.TaskPending || task.MaxAttempts != 2 {
		t.Fatalf("task = %+v", task)
	}
}
