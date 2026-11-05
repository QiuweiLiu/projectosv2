package delivery_test

import (
	"os"
	"path/filepath"
	"testing"

	"projectos/kernel/internal/delivery"
	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/store"
	"projectos/kernel/internal/verify"
)

func TestAssembleWritesAcceptedArtifacts(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "v", ArtifactPath: "src/app.js"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Claim("T1", "r1", "fake"); !ok {
		t.Fatal("claim")
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunSucceeded, Accepted: true,
		Result: "console.log(1)"}); err != nil {
		t.Fatal(err)
	}
	n, err := delivery.Assemble(st, dir)
	if err != nil || n != 1 {
		t.Fatalf("assemble = %d, %v", n, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "out", "src", "app.js"))
	if err != nil || string(raw) != "console.log(1)" {
		t.Fatalf("artifact = %q, %v", raw, err)
	}
}

func TestVerifyAssembledReportsFailingCommand(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "v", ArtifactPath: "a.txt",
		Commands: []verify.Spec{{Type: "command", Command: "go test"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Claim("T1", "r1", "fake"); !ok {
		t.Fatal("claim")
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunSucceeded, Accepted: true,
		Result: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.Assemble(st, dir); err != nil {
		t.Fatal(err)
	}

	// A delivered tree with no Go sources cannot pass `go test`: the check must
	// report that instead of trusting per-task acceptance.
	checks, err := delivery.VerifyAssembled(st, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].OK {
		t.Fatalf("checks = %+v, want one failure", checks)
	}
	if checks[0].Message == "" {
		t.Fatal("failure must carry the command output")
	}
}

func TestVerifyAssembledSkipsTasksWithoutCommands(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "v", ArtifactPath: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Claim("T1", "r1", "fake"); !ok {
		t.Fatal("claim")
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunSucceeded, Accepted: true,
		Result: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.Assemble(st, dir); err != nil {
		t.Fatal(err)
	}
	checks, err := delivery.VerifyAssembled(st, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 0 {
		t.Fatalf("checks = %+v, want none", checks)
	}
}

func TestListGivesPreviewsAndSkipsBinary(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "out", "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "out", "b.bin"), []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := delivery.List(dir, 10, 100)
	if err != nil || len(files) != 2 {
		t.Fatalf("files = %+v, %v", files, err)
	}
	if files[0].Path != "a.txt" || files[0].Preview != "hello" {
		t.Fatalf("text preview = %+v", files[0])
	}
	if files[1].Path != "b.bin" || files[1].Preview != "" {
		t.Fatalf("binary preview = %+v", files[1])
	}
}

// Writing an artifact whose final path is a symlink must fail instead of
// overwriting the link target.
func TestAssembleRefusesSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(outside, []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "out", "a.txt")); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateTask(domain.Task{ID: "T1", Validator: "v", ArtifactPath: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Claim("T1", "r1", "fake"); !ok {
		t.Fatal("claim")
	}
	if _, err := st.SettleRun(store.Settlement{RunID: "r1", RunStatus: domain.RunSucceeded, Accepted: true, Result: "pwned"}); err != nil {
		t.Fatal(err)
	}

	if _, err := delivery.Assemble(st, dir); err == nil {
		t.Fatal("assembly followed a symlinked target")
	}
	raw, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "safe" {
		t.Fatalf("outside file was overwritten: %q", raw)
	}
}
