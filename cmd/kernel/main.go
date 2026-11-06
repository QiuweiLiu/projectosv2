// Command kernel is the minimal CLI: run an objective JSON, then inspect state.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"projectos/kernel/internal/aoenv"
	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/engine"
	"projectos/kernel/internal/exec/ao"
	"projectos/kernel/internal/objective"
	"projectos/kernel/internal/policy"
	"projectos/kernel/internal/store"
)

const (
	exitOK      = 0
	exitError   = 1
	exitBlocked = 2
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kernel <run|status|serve|goal|project> [flags]")
		return exitError
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:])
	case "status":
		return cmdStatus(args[1:])
	case "serve":
		return cmdServe(args[1:])
	case "goal":
		return cmdGoal(args[1:])
	case "project":
		return cmdProject(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		return exitError
	}
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	objPath := fs.String("objective", "", "objective JSON file")
	dir := fs.String("dir", "", "state directory")
	model := fs.String("model", "deepseek/deepseek-flash", "provider/model")
	reviewerOn := fs.Bool("reviewer", false, "use a real agent reviewer")
	maxRuns := fs.Int("max-runs", 0, "maximum runs (0 = unlimited)")
	maxReviews := fs.Int("max-reviews", 0, "maximum reviewer sessions (0 = unlimited)")
	maxTokens := fs.Int("max-tokens", 0, "estimated token budget, chars/4 (0 = unlimited)")
	dryRun := fs.Bool("dry-run", false, "validate the objective and exit without calling any model")
	interval := fs.Duration("interval", 3*time.Second, "poll interval")
	limit := fs.Duration("timeout", 30*time.Minute, "overall budget")
	runTimeout := fs.Duration("run-timeout", 20*time.Minute, "per-run timeout")
	clearQ := fs.Bool("clear-quarantine", false, "clear a persisted quarantine after a human checked the workers")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if *objPath == "" || *dir == "" {
		fmt.Fprintln(os.Stderr, "run requires --objective and --dir")
		return exitError
	}

	raw, err := os.ReadFile(*objPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "objective:", err)
		return exitError
	}
	obj, err := objective.Parse(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "objective:", err)
		return exitError
	}
	pack, err := policy.Get(obj.Pack)
	if err != nil {
		fmt.Fprintln(os.Stderr, "policy:", err)
		return exitError
	}
	if pack.RequireReview && !*reviewerOn {
		fmt.Fprintf(os.Stderr, "policy pack %q requires --reviewer\n", pack.Name)
		return exitError
	}
	obj = obj.ApplyPack(pack)

	if *dryRun {
		fmt.Printf("objective %q is valid: %d tasks\n", obj.Name, len(obj.Tasks))
		for _, t := range obj.Tasks {
			fmt.Printf("  %-3s deps=%v artifact=%s validator=%s\n", t.ID, t.Deps, t.Artifact, t.Validator.Type)
		}
		return exitOK
	}

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitError
	}
	st, err := store.Open(filepath.Join(*dir, "kernel.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return exitError
	}
	defer st.Close()

	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	resume, err := resolveRunState(st, obj, hash, pack.Name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state:", err)
		return exitError
	}
	if *clearQ {
		if err := st.SetMeta("quarantine", ""); err != nil {
			fmt.Fprintln(os.Stderr, "quarantine:", err)
			return exitError
		}
	}
	if reason, _ := st.GetMeta("quarantine"); reason != "" && !*clearQ {
		fmt.Fprintf(os.Stderr, "state is quarantined: %s\nrerun with --clear-quarantine after checking the workers\n", reason)
		return exitError
	}
	if resume {
		fmt.Printf("resuming objective %s (%s)\n", obj.Name, shortHash(hash))
	} else {
		for key, value := range map[string]string{
			"objective_name": obj.Name,
			"objective_hash": hash,
			"objective_path": *objPath,
			"policy_pack":    pack.Name,
		} {
			if err := st.SetMeta(key, value); err != nil {
				fmt.Fprintln(os.Stderr, "meta:", err)
				return exitError
			}
		}
	}

	env, err := aoenv.Setup(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ao env:", err)
		return exitError
	}
	defer env.Close()

	projectID, err := env.CreateProject()
	if err != nil {
		fmt.Fprintln(os.Stderr, "project:", err)
		return exitError
	}
	fmt.Printf("project=%s port=%d dir=%s\n", projectID, env.Port, *dir)

	backend := ao.New(ao.Config{BaseURL: env.BaseURL, ProjectID: projectID, Harness: "opencode-v2",
		Model: *model, DataDir: env.DataDir})
	e := engine.New(st, backend, 2, obj.Validators()).
		WithBudget(engine.Budget{MaxRuns: *maxRuns, MaxReviews: *maxReviews, MaxTokens: *maxTokens}).
		WithRunTimeout(*runTimeout)
	if *reviewerOn {
		e = e.WithReviewer(ao.NewReviewer(backend))
	}
	if !resume {
		for _, task := range obj.DomainTasks() {
			if err := e.Add(task); err != nil {
				fmt.Fprintln(os.Stderr, "add task:", err)
				return exitError
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *limit)
	defer cancel()
	for {
		if _, err := e.Step(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "step:", err)
			return exitError
		}
		tasks, err := st.List()
		if err != nil {
			fmt.Fprintln(os.Stderr, "list:", err)
			return exitError
		}
		if len(tasks) != len(obj.Tasks) {
			fmt.Fprintf(os.Stderr, "state has %d tasks, objective has %d\n", len(tasks), len(obj.Tasks))
			return exitError
		}
		if terminal(tasks) {
			return finish(tasks)
		}
		select {
		case <-ctx.Done():
			abortCtx, abortCancel := context.WithTimeout(context.Background(), 30*time.Second)
			abandoned, aerr := e.Abort(abortCtx, "objective timeout")
			abortCancel()
			if aerr != nil {
				fmt.Fprintln(os.Stderr, "abort:", aerr)
			} else {
				fmt.Fprintf(os.Stderr, "objective timed out; aborted %d in-flight runs\n", abandoned)
			}
			return exitError
		case <-time.After(*interval):
		}
	}
}

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "status requires --dir")
		return exitError
	}
	st, err := store.OpenReadOnly(filepath.Join(*dir, "kernel.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return exitError
	}
	defer st.Close()

	view, err := collectStatus(st)
	if err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
		return exitError
	}
	if *asJSON {
		if err := writeJSON(os.Stdout, view); err != nil {
			fmt.Fprintln(os.Stderr, "encode:", err)
			return exitError
		}
		return exitOK
	}
	label := "tokens(measured)"
	if view.Estimated {
		label = "tokens(estimated)"
	}
	fmt.Printf("objective=%s hash=%s pack=%s %s=%d\n",
		view.ObjectiveName, shortHash(view.ObjectiveHash), view.Pack, label, view.PromptTokens+view.OutputTokens)
	for _, r := range view.Tasks {
		fmt.Printf("%-3s %-9s attempts=%d evidence=%d reviews=%d blocking=%d %s\n",
			r.ID, r.Status, r.Attempts, r.Evidence, r.Reviews, r.Blocking, r.Error)
	}
	return exitOK
}

// resolveRunState decides whether a state directory can be resumed. Reusing a
// directory for a different objective, or one with tasks but no metadata, must
// fail instead of silently overwriting the previous run's identity.
func resolveRunState(st *store.Store, obj objective.Objective, hash, pack string) (bool, error) {
	existing, err := st.GetMeta("objective_hash")
	if err != nil {
		return false, err
	}
	tasks, err := st.List()
	if err != nil {
		return false, err
	}
	if existing == "" {
		if len(tasks) > 0 {
			return false, fmt.Errorf("state dir already has %d tasks but no objective metadata", len(tasks))
		}
		return false, nil
	}
	if existing != hash {
		return false, fmt.Errorf("state dir belongs to a different objective (hash %s), use a new --dir", shortHash(existing))
	}
	if stored, err := st.GetMeta("policy_pack"); err != nil {
		return false, err
	} else if stored != "" && stored != pack {
		return false, fmt.Errorf("state dir used policy pack %q, current objective uses %q", stored, pack)
	}
	want := map[domain.TaskID]bool{}
	for _, t := range obj.DomainTasks() {
		want[t.ID] = true
	}
	for _, t := range tasks {
		if !want[t.ID] {
			return false, fmt.Errorf("state dir has task %s that the objective does not define", t.ID)
		}
		delete(want, t.ID)
	}
	if len(want) > 0 {
		return false, fmt.Errorf("state dir is missing %d tasks from the objective", len(want))
	}
	return true, nil
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func finish(tasks []domain.Task) int {
	blocked := false
	for _, t := range tasks {
		if t.Status != domain.TaskAccepted {
			blocked = true
		}
		fmt.Printf("task %-3s %-9s attempts=%d %s\n", t.ID, t.Status, t.Attempts, t.LastError)
	}
	if blocked {
		return exitBlocked
	}
	return exitOK
}

func terminal(tasks []domain.Task) bool {
	if len(tasks) == 0 {
		return false
	}
	for _, t := range tasks {
		if t.Status != domain.TaskAccepted && t.Status != domain.TaskBlocked && t.Status != domain.TaskSuperseded {
			return false
		}
	}
	return true
}
