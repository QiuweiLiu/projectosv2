package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestServeFileStaysInsideOut(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(filepath.Join(out, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "sub", "b.txt"), []byte("nested"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink inside out/ that points outside must never be served.
	secret := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(out, "link.txt")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		code int
		body string
	}{
		{"/files/a.txt", 200, "hello"},
		{"/files/sub/b.txt", 200, "nested"},
		{"/files/../secret.txt", 404, ""},
		{"/files/..%2fsecret.txt", 404, ""}, // decoded to ../secret.txt, still confined to out/
		{"/files/link.txt", 403, ""},
		{"/files/missing.txt", 404, ""},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", c.path, nil)
		serveFile(rec, req, newWorkspace(dir))
		if rec.Code != c.code {
			t.Fatalf("%s = %d, want %d", c.path, rec.Code, c.code)
		}
		if c.body != "" && rec.Body.String() != c.body {
			t.Fatalf("%s body = %q", c.path, rec.Body.String())
		}
	}
}

func TestServeFileSupportsRange(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "v.mp4"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/files/v.mp4", nil)
	req.Header.Set("Range", "bytes=2-5")
	serveFile(rec, req, newWorkspace(dir))
	if rec.Code != 206 {
		t.Fatalf("range status = %d", rec.Code)
	}
	if rec.Body.String() != "2345" {
		t.Fatalf("range body = %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("content type = %q", got)
	}
}
