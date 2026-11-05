package main

import (
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// serveFile streams one file from <objective>/out. It is read-only, confined to
// that tree (symlinks resolved), and uses ServeContent so Range requests work for
// video and audio. The first path segment names the objective; legacy links omit
// it and resolve only when the workspace has a single objective.
func serveFile(w http.ResponseWriter, r *http.Request, ws *workspace) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/files/")
	if rel == "" {
		http.NotFound(w, r)
		return
	}
	first, rest, _ := strings.Cut(rel, "/")
	p, ok, _ := ws.find(first)
	if ok {
		rel = rest
	} else {
		p, ok, _ = ws.resolve("")
	}
	if !ok {
		// Legacy callers pass a bare /files/<name>; keep serving the workspace
		// root so old bookmarks do not break.
		p = project{Dir: ws.root}
	}
	if strings.TrimSpace(rel) == "" {
		http.NotFound(w, r)
		return
	}
	root := filepath.Join(p.Dir, "out")
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Clean with slash semantics so "../" cannot climb out before we join.
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	if rel == "" || rel == "." {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if resolved != resolvedRoot && !strings.HasPrefix(resolved, resolvedRoot+string(os.PathSeparator)) {
		http.Error(w, "outside the delivery tree", http.StatusForbidden)
		return
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(resolved)
	if err != nil {
		http.Error(w, "cannot read file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	ctype := mime.TypeByExtension(filepath.Ext(resolved))
	if ctype == "" {
		// .md/.csv and friends are unknown to the system mime table; sniff the
		// first bytes so text opens as text instead of a download.
		head := make([]byte, 512)
		n, _ := file.Read(head)
		ctype = http.DetectContentType(head[:n])
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			http.Error(w, "cannot read file", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, filepath.Base(resolved), info.ModTime(), file)
}
