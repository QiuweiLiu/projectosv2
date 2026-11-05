package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"projectos/kernel/internal/diff"
	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/store"
)

type diffView struct {
	Task     string `json:"task"`
	Path     string `json:"path"`
	Current  string `json:"current,omitempty"`
	Previous string `json:"previous,omitempty"`
	Diff     string `json:"diff,omitempty"`
	First    bool   `json:"first"`
	Binary   bool   `json:"binary"`
	Changed  bool   `json:"changed"`
	TooLarge bool   `json:"tooLarge,omitempty"`
}

// serveDiff compares the newest attempt with the previous delivery first, then
// with the previous attempt. It never writes: this is evidence for a human, not
// a merge tool.
func serveDiff(w http.ResponseWriter, r *http.Request, ws *workspace) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	p, ok, err := ws.resolve(r.URL.Query().Get("objective"))
	if err != nil || !ok {
		http.Error(w, "objective is required", http.StatusBadRequest)
		return
	}
	taskID := domain.TaskID(strings.TrimSpace(r.URL.Query().Get("task")))
	if taskID == "" {
		http.Error(w, "task is required", http.StatusBadRequest)
		return
	}
	st, err := store.OpenReadOnly(filepath.Join(p.Dir, "kernel.db"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()
	task, ok, err := st.Get(taskID)
	if err != nil || !ok {
		http.Error(w, "unknown task", http.StatusNotFound)
		return
	}
	rel := filepath.ToSlash(strings.TrimSpace(task.ArtifactPath))
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
		http.Error(w, "task has no comparable artifact", http.StatusBadRequest)
		return
	}
	view := diffView{Task: string(taskID), Path: rel}

	dirs := workDirs(p.Dir, string(taskID))
	outBase := filepath.Join(p.Dir, "out")
	currentBase := ""
	if len(dirs) > 0 {
		currentBase = dirs[0]
	}
	current := ""
	if currentBase != "" {
		current = filepath.Join(currentBase, filepath.FromSlash(rel))
	}
	if !fileExists(current) && fileExists(filepath.Join(outBase, filepath.FromSlash(rel))) {
		currentBase = outBase
		current = filepath.Join(outBase, filepath.FromSlash(rel))
	}
	if !fileExists(current) {
		view.First = true
		writeJSON(w, view)
		return
	}
	view.Current = current

	previousBase := ""
	if currentBase != outBase && fileExists(filepath.Join(outBase, filepath.FromSlash(rel))) {
		previousBase = outBase
	} else if len(dirs) > 1 {
		candidate := filepath.Join(dirs[1], filepath.FromSlash(rel))
		if fileExists(candidate) {
			previousBase = dirs[1]
		}
	}
	previous := ""
	if previousBase != "" {
		previous = filepath.Join(previousBase, filepath.FromSlash(rel))
	}
	if previous == "" || previous == current {
		view.First = true
		writeJSON(w, view)
		return
	}
	view.Previous = previous

	oldRaw, err := readConfined(previousBase, rel)
	if err != nil {
		http.Error(w, "cannot read previous artifact", http.StatusForbidden)
		return
	}
	newRaw, err := readConfined(currentBase, rel)
	if err != nil {
		http.Error(w, "cannot read current artifact", http.StatusForbidden)
		return
	}
	if bytes.IndexByte(newRaw, 0) >= 0 || !utf8.Valid(newRaw) {
		view.Binary = true
		view.Changed = !bytes.Equal(oldRaw, newRaw)
		writeJSON(w, view)
		return
	}
	lines, derr := diff.Text(string(oldRaw), string(newRaw))
	if derr != nil {
		view.TooLarge = true
		writeJSON(w, view)
		return
	}
	view.Diff = diff.Render(lines)
	view.Changed = diff.Changed(lines)
	w.Header().Set("Content-Type", "application/json")
	_ = writeJSON(w, view)
}

// workDirs returns attempt directories for one task, newest first. The task id
// is a prefix, not a substring: T1 must not match T10.
func workDirs(dir, taskID string) []string {
	root := filepath.Join(dir, "work")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	type item struct {
		path string
		mod  int64
	}
	var out []item
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, taskID+"-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, item{path: filepath.Join(root, name), mod: info.ModTime().UnixNano()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mod > out[j].mod })
	dirs := make([]string, 0, len(out))
	for _, item := range out {
		dirs = append(dirs, item.path)
	}
	return dirs
}

// readConfined refuses symlinks that escape the attempt/delivery directory. The
// diff endpoint is unauthenticated, so a worker-created symlink must not turn it
// into a file-read primitive.
func readConfined(base, name string) ([]byte, error) {
	root, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(base, name)
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return nil, err
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
		return nil, os.ErrPermission
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(resolved)
}
