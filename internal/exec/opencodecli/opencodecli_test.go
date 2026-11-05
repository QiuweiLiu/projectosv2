package opencodecli

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/exec"
)

func TestSeedAndChangedFiles(t *testing.T) {
	base := t.TempDir()
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, ".git", "HEAD"), []byte("ref"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "keep.txt"), []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "edit.txt"), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}

	seed, err := seedDir(base, work)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, ".git")); !os.IsNotExist(err) {
		t.Fatal(".git must not be copied into the task dir")
	}

	// The worker edits one file and creates another.
	if err := os.WriteFile(filepath.Join(work, "edit.txt"), []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".kernel-run.log"), []byte("log"), 0o644); err != nil {
		t.Fatal(err)
	}

	changed := changedFiles(work, seed)
	got := strings.Join(changed, ",")
	if got != "edit.txt,new.txt" {
		t.Fatalf("changed = %q", got)
	}
}

func TestParseOutputReadsTextAndTokens(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, ".kernel-run.log")
	body := `{"type":"step_start","part":{"type":"step-start"}}
{"type":"text","part":{"type":"text","text":"收到"}}
{"type":"step_finish","part":{},"tokens":{"input":120,"output":6}}
`
	if err := os.WriteFile(log, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	text, u := parseOutput(log)
	if text != "收到" {
		t.Fatalf("text = %q", text)
	}
	if u.input != 120 || u.output != 6 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestReapStaleKillsOrphanProcess(t *testing.T) {
	work := t.TempDir()
	dir := filepath.Join(work, "orphan")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := osexec.Command("sh", "-c", "exec sleep 30")
	cmd.Dir = dir // same shape as the real backend: cwd is set at spawn
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, ".kernel-run.pid")
	if err := os.WriteFile(pidFile, []byte(fmt.Sprint(cmd.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}

	New(Config{WorkRoot: work})

	// Wait reaps the child and tells us whether it was killed rather than exiting
	// on its own; Signal(0) alone would also succeed for a zombie.
	if err := cmd.Wait(); err == nil {
		t.Fatal("orphaned process exited normally; it was not killed")
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatal("pid file was not cleared")
	}
}

// The fake CLI records its arguments and emits usage events.
func fakeCLI(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-opencode.sh")
	script := `#!/bin/sh
printf '%s\n' "$@" > args.txt
printf 'hello' > hello.txt
echo '{"type":"text","sessionID":"ses_fake","part":{"type":"text","text":"ok"}}'
echo '{"type":"step_finish","part":{"type":"step-finish","tokens":{"input":10,"output":25}}}'
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitDone(t *testing.T, b *Backend, id string) {
	t.Helper()
	for i := 0; i < 300; i++ {
		state, err := b.Observe(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if state.Status != domain.RunRunning {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not finish")
}

func TestApprovalModeAndUsageWithArtifact(t *testing.T) {
	work := t.TempDir()
	b := New(Config{Model: "provider/model", Bin: fakeCLI(t), WorkRoot: work})

	// Read-only role: no --auto, even though it produces a file here.
	start, err := b.Start(context.Background(), exec.StartRequest{
		TaskID: "T1", Attempt: 1, Prompt: "p", ApprovalMode: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, b, start.ExternalID)
	args, err := os.ReadFile(filepath.Join(work, start.ExternalID, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), "--auto") {
		t.Fatalf("read-only role got --auto: %s", args)
	}
	artifact, err := b.Collect(context.Background(), start.ExternalID, "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Result != "hello" {
		t.Fatalf("artifact = %q", artifact.Result)
	}
	if artifact.OutputChars != 100 {
		t.Fatalf("usage was not collected with an artifact: %+v", artifact)
	}
	if artifact.PromptTokens != 10 || artifact.OutputTokens != 25 || artifact.UsageEstimated {
		t.Fatalf("provider tokens were not kept: %+v", artifact)
	}
	if artifact.SessionID != "ses_fake" {
		t.Fatalf("session id was not captured: %+v", artifact)
	}

	// Retry: the previous session id must reach the CLI as --session.
	start, err = b.Start(context.Background(), exec.StartRequest{
		TaskID: "T1", Attempt: 2, Prompt: "fix it", SessionID: "ses_previous",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, b, start.ExternalID)
	args, err = os.ReadFile(filepath.Join(work, start.ExternalID, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--session\nses_previous") {
		t.Fatalf("retry did not resume the session: %s", args)
	}

	// Worker: auto-approval is expected.
	start, err = b.Start(context.Background(), exec.StartRequest{TaskID: "T2", Attempt: 1, Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, b, start.ExternalID)
	args, err = os.ReadFile(filepath.Join(work, start.ExternalID, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--auto") {
		t.Fatalf("worker did not get --auto: %s", args)
	}
}
