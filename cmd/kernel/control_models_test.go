package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"projectos/kernel/internal/store"
)

func TestUpdateModelsVariantDoesNotClearModel(t *testing.T) {
	root := t.TempDir()
	argsFile := filepath.Join(root, "args.txt")
	bin := filepath.Join(root, "fake-kernel.sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argsFile+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctl := newControl(bin, newWorkspace(root))
	req := httptest.NewRequest(http.MethodPost, "/api/models", strings.NewReader(`{"role":"chat","variant":"high"}`))
	req.Header.Set("X-Kernel-Token", ctl.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ctl.updateModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if strings.Contains(got, "--set") {
		t.Fatalf("variant request cleared the model: %q", got)
	}
	if !strings.Contains(got, "--variant") || !strings.Contains(got, "chat=high") {
		t.Fatalf("variant args = %q", got)
	}
}

func TestGoalCreateUsesExplicitID(t *testing.T) {
	dir := t.TempDir()
	if code := goalCreate([]string{"--dir", dir, "--goal", "explicit id", "--id", "obj-explicit"}); code != exitOK {
		t.Fatalf("goal create = %d", code)
	}
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	objs, err := st.ListObjectives()
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || objs[0].ID != "obj-explicit" {
		t.Fatalf("objectives = %+v", objs)
	}
}
