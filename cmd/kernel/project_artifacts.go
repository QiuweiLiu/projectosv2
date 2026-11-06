package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// seedProjectArtifacts exposes accepted artifacts from other objectives in a
// read-only namespace inside the task seed directory. The shared copy is
// namespaced by objective id, so two objectives cannot overwrite each other.
func seedProjectArtifacts(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "project.json"))
	if err != nil {
		return nil
	}
	var meta struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &meta) != nil || strings.TrimSpace(meta.ID) == "" {
		return nil
	}
	root := dir
	parent := filepath.Dir(dir)
	if filepath.Base(parent) == "projects" {
		root = filepath.Dir(parent)
	}
	shared := filepath.Join(root, "project-state", meta.ID, "artifacts")
	if _, err := os.Stat(shared); err != nil {
		return nil
	}
	dest := filepath.Join(dir, "repo", "project-artifacts")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	return copySharedTree(shared, dest)
}

func copySharedTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 5<<20 {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		return os.WriteFile(target, raw, 0o644)
	})
}
