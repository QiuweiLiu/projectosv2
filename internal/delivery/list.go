package delivery

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// File is one delivered artifact as the cockpit shows it: path, size, and a
// short text preview. Binary files simply have no preview.
type File struct {
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`
	Kind    string `json:"kind"` // image | video | audio | pdf | text | binary
	Preview string `json:"preview,omitempty"`
}

// List reads <dir>/out. Preview bytes are capped so a large artifact cannot
// flood the page.
func List(dir string, maxFiles, previewBytes int) ([]File, error) {
	root := filepath.Join(dir, "out")
	if _, err := os.Stat(root); err != nil {
		return nil, nil
	}
	var files []File
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if len(files) >= maxFiles {
			return filepath.SkipAll
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		info, ierr := entry.Info()
		if ierr != nil {
			return ierr
		}
		file := File{Path: rel, Bytes: info.Size(), Kind: kindOf(rel)}
		if file.Kind == "text" {
			if preview, perr := readPreview(path, previewBytes); perr == nil {
				file.Preview = preview
			}
		}
		files = append(files, file)
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, err
}

var kinds = map[string]string{
	".png": "image", ".jpg": "image", ".jpeg": "image", ".gif": "image", ".webp": "image", ".svg": "image",
	".mp4": "video", ".webm": "video", ".mov": "video", ".m4v": "video",
	".mp3": "audio", ".wav": "audio", ".m4a": "audio", ".ogg": "audio",
	".pdf": "pdf",
	".md":  "text", ".txt": "text", ".csv": "text", ".tsv": "text", ".json": "text", ".yaml": "text", ".yml": "text",
	".js": "text", ".mjs": "text", ".cjs": "text", ".ts": "text", ".tsx": "text", ".jsx": "text",
	".py": "text", ".go": "text", ".rs": "text", ".java": "text", ".c": "text", ".h": "text", ".cpp": "text",
	".html": "text", ".css": "text", ".sh": "text", ".sql": "text", ".xml": "text", ".toml": "text", ".ini": "text",
}

func kindOf(name string) string {
	if kind, ok := kinds[strings.ToLower(filepath.Ext(name))]; ok {
		return kind
	}
	return "binary"
}

func readPreview(path string, limit int) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if int64(len(raw)) > int64(limit) {
		raw = raw[:limit]
	}
	text := string(raw)
	if strings.ContainsRune(text, 0) {
		return "", nil // binary
	}
	return strings.TrimSpace(text), nil
}

// RenderDeliverables is the plain-text form used by the reporter's model prompt.
func RenderDeliverables(files []File) string {
	if len(files) == 0 {
		return "（没有交付文件）"
	}
	var b strings.Builder
	for _, f := range files {
		b.WriteString("- " + f.Path)
		b.WriteString(" (")
		b.WriteString(humanBytes(f.Bytes))
		b.WriteString(")\n")
		if f.Preview != "" {
			preview := f.Preview
			if len(preview) > 200 {
				preview = preview[:200] + "…"
			}
			b.WriteString("  " + strings.ReplaceAll(preview, "\n", "\n  ") + "\n")
		}
	}
	return b.String()
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
