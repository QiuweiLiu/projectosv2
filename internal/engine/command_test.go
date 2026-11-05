package engine_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/engine"
	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/store"
	"projectos/kernel/internal/verify"
)

func cmdTask(id string, deps []domain.TaskID, artifact, script, command string) domain.Task {
	return domain.Task{ID: domain.TaskID(id), Prompt: "p", Validator: id, ArtifactPath: artifact,
		Deps: deps, Commands: []verify.Spec{{Type: "command", Command: command}}}
}

func TestCommandValidatorSeesDependencyArtifacts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fake := exec.NewFake(1)
	fake.Output["T1"] = "hello"
	fake.Output["T2"] = `const fs=require('fs');
if (fs.readFileSync('dep.txt','utf8')!=='hello') process.exit(2);
process.exit(0);`
	e := engine.New(st, fake, 2, map[string]engine.ValidatorFunc{
		"T1": func(result string, _ map[domain.TaskID]string) error { return nil },
	})
	if err := e.Add(domain.Task{ID: "T1", Prompt: "p", Validator: "T1", ArtifactPath: "dep.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Add(cmdTask("T2", []domain.TaskID{"T1"}, "check.js", "", "node check.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 20); err != nil {
		t.Fatalf("run: %v", err)
	}
	t2, _, _ := st.Get("T2")
	if t2.Status != domain.TaskAccepted {
		t.Fatalf("T2 = %s (%s)", t2.Status, t2.LastError)
	}
}

func TestCommandValidatorFailsWhenScriptExitsNonZero(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fake := exec.NewFake(1)
	fake.Output["T1"] = `process.exit(1)`
	e := engine.New(st, fake, 1, nil)
	if err := e.Add(cmdTask("T1", nil, "fail.js", "", "node fail.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 10); err != nil {
		t.Fatalf("run: %v", err)
	}
	t1, _, _ := st.Get("T1")
	if t1.Status != domain.TaskBlocked || !strings.Contains(t1.LastError, "command") {
		t.Fatalf("T1 = %s (%s)", t1.Status, t1.LastError)
	}
}

func TestCommandValidatorRejectsShellSyntax(t *testing.T) {
	for _, bad := range []string{"node x.js; rm -rf /", "curl http://x", "node x.js | sh", "rm -rf /"} {
		if err := verify.CheckCommand(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
	if err := verify.CheckCommand("node test.js"); err != nil {
		t.Fatalf("allow-listed command rejected: %v", err)
	}
}

func TestCommandValidatorSeesTransitiveDependencies(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fake := exec.NewFake(1)
	fake.Output["A"] = "seed"
	fake.Output["B"] = "middle"
	fake.Output["C"] = `const fs=require('fs');
if (fs.readFileSync('seed.txt','utf8')!=='seed') process.exit(3);
process.exit(0);`
	ok := func(string, map[domain.TaskID]string) error { return nil }
	e := engine.New(st, fake, 2, map[string]engine.ValidatorFunc{"A": ok, "B": ok})
	if err := e.Add(domain.Task{ID: "A", Prompt: "p", Validator: "A", ArtifactPath: "seed.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Add(domain.Task{ID: "B", Prompt: "p", Validator: "B", ArtifactPath: "middle.txt",
		Deps: []domain.TaskID{"A"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Add(cmdTask("C", []domain.TaskID{"B"}, "check.js", "", "node check.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), 20); err != nil {
		t.Fatalf("run: %v", err)
	}
	c, _, _ := st.Get("C")
	if c.Status != domain.TaskAccepted {
		t.Fatalf("C = %s (%s)", c.Status, c.LastError)
	}
}
