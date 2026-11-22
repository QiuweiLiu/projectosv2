package main

// goal replan is the explicit, operator-triggered counterpart of the bounded
// replan inside `goal run --replan`. `goal run` cannot help an objective that
// is already blocked before the loop starts: recovery asks for approval and
// exits. This command performs the replacement the operator asked for:
// accepted tasks are kept verbatim, the unfinished work is replaced by a new
// plan version, and the objective is activated again. It never runs a task.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"projectos/kernel/internal/compile"
	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/modelcfg"
	"projectos/kernel/internal/planner"
	"projectos/kernel/internal/recovery"
	"projectos/kernel/internal/replan"
	"projectos/kernel/internal/store"
)

func goalReplan(args []string) int {
	fs := flag.NewFlagSet("goal replan", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	which := fs.String("planner", "model", "model | fake")
	planFile := fs.String("plan-file", "", "replacement plan JSON for the fake planner")
	model := fs.String("model", "", "provider/model for the planner (default: settings or built-in)")
	executor := fs.String("executor", "opencode", "opencode | ao")
	maxTasks := fs.Int("max-tasks", 0, "task limit for the replacement (0 = keep the current size)")
	projectContext := fs.String("context", "", "bounded project context for the planner")
	instruction := fs.String("instruction", "", "operator instruction to include when regenerating the plan")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	st, err := openGoalStore(*dir)
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			fmt.Fprintf(os.Stderr, "another scheduler owns %s; stop it before replanning\n", *dir)
		} else {
			fmt.Fprintln(os.Stderr, "store:", err)
		}
		return exitError
	}
	defer st.Close()
	obj, ok, err := singleObjective(st)
	if err != nil || !ok {
		fmt.Fprintln(os.Stderr, "no objective in this state dir")
		return exitError
	}
	if obj.Status != goal.StatusActive && obj.Status != goal.StatusFailed {
		fmt.Fprintf(os.Stderr, "objective is %s; only an active or failed objective can be replanned\n", obj.Status)
		return exitError
	}
	record, ok, err := st.GetPlan(obj.ID, obj.PlanVersion)
	if err != nil || !ok {
		fmt.Fprintln(os.Stderr, "current plan not found")
		return exitError
	}
	tasks, err := st.List()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tasks:", err)
		return exitError
	}
	existing := map[domain.TaskID]bool{}
	var acceptedIDs []string
	blocked := 0
	for _, t := range tasks {
		existing[t.ID] = true
		if t.Status == domain.TaskAccepted {
			acceptedIDs = append(acceptedIDs, string(t.ID))
		}
		if t.Status == domain.TaskBlocked || t.Status == domain.TaskRejected {
			blocked++
		}
	}
	if blocked == 0 {
		fmt.Println("no blocked tasks; nothing to replan")
		return exitOK
	}
	if *which == "fake" && strings.TrimSpace(*planFile) == "" {
		fmt.Fprintln(os.Stderr, "the fake planner needs --plan-file")
		return exitError
	}

	feedback, failedIDs, acceptedFromPlan, err := replan.FailureContext(st, record.Spec)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replan context:", err)
		return exitError
	}
	if len(acceptedFromPlan) > 0 {
		acceptedIDs = acceptedFromPlan
	}
	if len(failedIDs) > 0 {
		names := make([]string, 0, len(failedIDs))
		for _, id := range failedIDs {
			names = append(names, string(id))
		}
		feedback += "\nThe operator asked for a fresh plan for the unfinished work. " +
			"Give every previously failed task a new id (for example T1b); do not reuse " +
			strings.Join(names, ", ") + "."
	}
	if strings.TrimSpace(*instruction) != "" {
		feedback += "\nThe operator added this instruction from chat: " +
			strings.TrimSpace(*instruction)
	}

	limit := *maxTasks
	if limit <= 0 {
		limit = len(record.Spec.Tasks)
	}
	if limit < 4 {
		limit = 4
	}

	var backend exec.Backend
	var closeBackend = func() {}
	if *which == "model" {
		plannerSettings := modelcfg.Load(*dir)
		plannerModel := modelcfg.WithVariant(resolveRoleModel(*dir, "planner", *model),
			plannerSettings.VariantFor("planner"))
		fmt.Printf("planner model: %s\n", plannerModel)
		backend, closeBackend, err = openSessionBackend(*dir, *executor, plannerModel)
		if err != nil {
			fmt.Fprintln(os.Stderr, "backend:", err)
			return exitError
		}
		defer closeBackend()
	}
	p := freshReplanner(*which, *planFile, backend)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var newPlan goal.Plan
	var replanUsage store.PlanUsage
	replanErr := error(nil)
	for attempt := 0; attempt < 2; attempt++ {
		candidate, planResult, gerr := planner.Generate(ctx, p, planner.Request{
			Goal: obj.Goal, Constraints: obj.Constraints, Expected: obj.ExpectedOutcome,
			Feedback: feedback, MaxTasks: limit, Context: *projectContext,
		}, 2)
		if gerr != nil {
			replanErr = gerr
			break
		}
		candidate = replan.NormalizeAccepted(record.Spec, candidate, acceptedIDs)
		if err := replan.ValidateReplacementLimit(record.Spec, candidate, acceptedIDs, limit); err != nil {
			replanErr = err
			feedback += "\nThe replacement was rejected: " + err.Error() +
				"\nRetired tasks must NOT be reused; create new ids such as T1b."
			p = freshReplanner(*which, *planFile, backend)
			continue
		}
		if err := rejectHistoricalID(candidate, record.Spec, existing); err != nil {
			replanErr = err
			feedback += "\nThe replacement was rejected: " + err.Error() +
				"\nRetired tasks must NOT be reused; create new ids such as T1b."
			p = freshReplanner(*which, *planFile, backend)
			continue
		}
		if err := compile.ValidatePlanForKernel(candidate); err != nil {
			replanErr = err
			feedback += "\nThe replacement was rejected: " + err.Error()
			p = freshReplanner(*which, *planFile, backend)
			continue
		}
		newPlan = candidate
		replanUsage = store.PlanUsage{
			PromptChars: planResult.PromptChars, OutputChars: planResult.OutputChars,
			PromptTokens: planResult.PromptTokens, OutputTokens: planResult.OutputTokens,
			UsageEstimated: planResult.UsageEstimated,
		}
		replanErr = nil
		break
	}
	if replanErr != nil {
		fmt.Fprintln(os.Stderr, "replan rejected:", replanErr)
		fmt.Println("the existing plan was left untouched")
		return exitBlocked
	}
	hash, err := planner.Hash(newPlan)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hash:", err)
		return exitError
	}
	version := obj.PlanVersion + 1
	retired, err := replan.Apply(st, obj.ID, version, record.Spec, newPlan, hash, replanUsage)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replan apply:", err)
		return exitError
	}
	_ = recovery.ClearPending(st)
	_ = st.AppendEvent("", "replanned",
		fmt.Sprintf("plan v%d (%d tasks retired)", version, retired))
	created := compile.Tasks(newPlan, obj.ID, version)
	for _, task := range created {
		if existing[task.ID] {
			continue
		}
		if err := st.CreateTask(task); err != nil {
			fmt.Fprintln(os.Stderr, "add task:", err)
			return exitError
		}
		existing[task.ID] = true
	}
	fmt.Printf("replanned to v%d (%d tasks retired); objective is active\n", version, retired)
	for _, task := range created {
		deps := make([]string, 0, len(task.Deps))
		for _, dep := range task.Deps {
			deps = append(deps, string(dep))
		}
		fmt.Printf("  %-8s deps=%-12s artifact=%s\n", task.ID,
			strings.Join(deps, ","), task.ArtifactPath)
	}
	fmt.Println("next: kernel goal run --dir " + *dir)
	return exitOK
}
