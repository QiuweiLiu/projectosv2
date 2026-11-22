package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthorisedChecksTokenAndOriginHostPort(t *testing.T) {
	c := &controlServer{token: "secret"}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8799/api/status", nil)
	req.Host = "127.0.0.1:8799"
	req.Header.Set("X-Kernel-Token", "secret")
	if !c.authorised(req) {
		t.Fatal("matching token and no origin rejected")
	}
	req.Header.Set("Origin", "http://127.0.0.1:8799")
	if !c.authorised(req) {
		t.Fatal("same-origin request rejected")
	}
	req.Header.Set("Origin", "http://127.0.0.1:8800")
	if c.authorised(req) {
		t.Fatal("cross-port origin accepted")
	}
	req.Header.Set("Origin", "http://evil.example")
	if c.authorised(req) {
		t.Fatal("foreign origin accepted")
	}
	req.Header.Set("Origin", "")
	req.Header.Set("X-Kernel-Token", "wrong")
	if c.authorised(req) {
		t.Fatal("wrong token accepted")
	}
}

func TestRequireTokenProtectsReadOnlyRoutes(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := requireToken(ok, &controlServer{token: "secret"})
	for _, path := range []string{"/api/status", "/api/objectives", "/files/a.html"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8799"+path, nil)
		req.Host = "127.0.0.1:8799"
		h.ServeHTTP(rec, req)
		if path == "/api/status" {
			if rec.Code != http.StatusOK {
				t.Fatalf("%s without token = %d", path, rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without token = %d, want 401", path, rec.Code)
		}
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8799"+path, nil)
		req.Host = "127.0.0.1:8799"
		req.Header.Set("X-Kernel-Token", "secret")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s with token = %d", path, rec.Code)
		}
	}
}

func TestRequireLoopbackHostRejectsRebinding(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := requireLoopbackHost(ok)
	for _, host := range []string{"127.0.0.1:8799", "localhost:8799", "[::1]:8799"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil)
		req.Host = host
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", host, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://evil.example/", nil)
	req.Host = "evil.example"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("rebound host = %d", rec.Code)
	}
}

func TestRemoteRequiresControl(t *testing.T) {
	dir := t.TempDir()
	if code := cmdServe([]string{"--dir", dir, "--addr", "0.0.0.0:0", "--allow-remote"}); code == exitOK {
		t.Fatal("allow-remote without control succeeded")
	}
}

func TestServeFileSetsSandboxCSP(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "a.html"), []byte("<script>alert(1)</script>"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/files/a.html", nil)
	serveFile(rec, req, newWorkspace(dir))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "connect-src 'none'") {
		t.Fatalf("csp = %q", csp)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff = %q", got)
	}
}
