package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"projectos/kernel/internal/aoenv"
	"projectos/kernel/internal/compile"
	"projectos/kernel/internal/delivery"
	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/engine"
	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/exec/ao"
	"projectos/kernel/internal/exec/opencodecli"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/modelcfg"
	"projectos/kernel/internal/planner"
	"projectos/kernel/internal/progress"
	"projectos/kernel/internal/projectstate"
	"projectos/kernel/internal/recovery"
	"projectos/kernel/internal/replan"
	"projectos/kernel/internal/report"
	"projectos/kernel/internal/store"
	"projectos/kernel/internal/verify"
)

// defaultModel is the built-in fallback when neither a flag nor models.json
// names a model. Change it here only; every role resolves through it.
const defaultModel = "opencode-go/deepseek-v4.1-flash"

// aoBackend builds a model session backend. The model string is passed straight
// to the harness, so any provider/model registered there can be used.
func aoBackend(env *aoenv.Env, projectID, model string) *ao.Backend {
	if strings.TrimSpace(model) == "" {
		model = defaultModel
	}
	harness := os.Getenv("KERNEL_AO_HARNESS")
	if harness == "" {
		harness = "opencode-v2"
	}
	return ao.New(ao.Config{BaseURL: env.BaseURL, ProjectID: projectID, Harness: harness,
		Model: model, DataDir: env.DataDir})
}

// opencodeBin resolves the OpenCode CLI every role should use. Chat already
// honours OPENCODE_BIN; planner/worker/reviewer must use the same binary or a
// v1 CLI on PATH can reject a v2 config and fail the whole objective.
func opencodeBin() string { return strings.TrimSpace(os.Getenv("OPENCODE_BIN")) }

// openSessionBackend builds a single-purpose backend for planner/reporter work.
// "opencode" needs no daemon; "ao" starts or reuses one.
func openSessionBackend(dir, executor, model string) (exec.Backend, func(), error) {
	if executor == "opencode" {
		return opencodecli.New(opencodecli.Config{
			Model: model, Bin: opencodeBin(), BaseDir: filepath.Join(dir, "repo"), WorkRoot: filepath.Join(dir, "work"),
		}), func() {}, nil
	}
	env, err := aoenv.Setup(dir)
	if err != nil {
		return nil, func() {}, err
	}
	projectID, err := env.CreateProject()
	if err != nil {
		env.Close()
		return nil, func() {}, err
	}
	return aoBackend(env, projectID, model), env.Close, nil
}

// resolveRoleModel picks the model for a role: explicit flag first, then the
// objective's saved settings, then the built-in default.
func resolveRoleModel(dir, role, explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	s := modelcfg.Load(dir)
	switch role {
	case "planner":
		if s.Planner != "" {
			return s.Planner
		}
	case "worker":
		if s.Worker != "" {
			return s.Worker
		}
	case "reviewer":
		if s.Reviewer != "" {
			return s.Reviewer
		}
	case "acceptance":
		if s.Acceptance != "" {
			return s.Acceptance
		}
	case "reporter":
		if s.Reporter != "" {
			return s.Reporter
		}
	}
	return defaultModel
}

func cmdGoal(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kernel goal <create|plan|show|confirm|run|status|progress|recover|metrics|export|resume|cancel|pause|unpause|report|explain|continue|models|prune> [flags]")
		return exitError
	}
	switch args[0] {
	case "create":
		return goalCreate(args[1:])
	case "plan":
		return goalPlan(args[1:])
	case "show":
		return goalShow(args[1:])
	case "confirm":
		return goalConfirm(args[1:])
	case "run":
		return goalRun(args[1:])
	case "status":
		return goalStatus(args[1:])
	case "progress":
		return goalProgress(args[1:])
	case "recover":
		return goalRecover(args[1:])
	case "metrics":
		return goalMetrics(args[1:])
	case "export":
		return goalExport(args[1:])
	case "resume":
		return goalResume(args[1:])
	case "unpause":
		return goalUnpause(args[1:])
	case "cancel":
		return goalCancel(args[1:])
	case "report":
		return goalReport(args[1:])
	case "explain":
		return goalExplain(args[1:])
	case "continue":
		return goalContinue(args[1:])
	case "models":
		return goalModels(args[1:])
	case "prune":
		return goalPrune(args[1:])
	case "pause":
		return goalPause(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown goal command %q\n", args[0])
		return exitError
	}
}

func openGoalStore(dir string) (*store.Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("--dir is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return store.Open(filepath.Join(dir, "kernel.db"))
}

// openGoalStoreRead opens without the scheduler lock. Read-side commands must
// keep working while a run is in flight, and they must never trigger recovery.
func openGoalStoreRead(dir string) (*store.Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("--dir is required")
	}
	return store.OpenReadOnly(filepath.Join(dir, "kernel.db"))
}

func singleObjective(st *store.Store) (goal.Objective, bool, error) {
	all, err := st.ListObjectives()
	if err != nil {
		return goal.Objective{}, false, err
	}
	switch len(all) {
	case 0:
		return goal.Objective{}, false, nil
	case 1:
		return all[0], true, nil
	default:
		return goal.Objective{}, false, fmt.Errorf("state dir has %d objectives; P1 keeps one objective per state dir", len(all))
	}
}

func goalCreate(args []string) int {
	fs := flag.NewFlagSet("goal create", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	text := fs.String("goal", "", "natural-language goal")
	title := fs.String("title", "", "short title")
	constraints := fs.String("constraints", "", "constraints")
	expected := fs.String("expected", "", "expected outcome")
	acceptance := fs.String("acceptance", "", "objective-level acceptance")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if strings.TrimSpace(*text) == "" {
		fmt.Fprintln(os.Stderr, "goal create requires --goal")
		return exitError
	}
	st, err := openGoalStore(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return exitError
	}
	defer st.Close()
	if existing, ok, err := singleObjective(st); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitError
	} else if ok {
		fmt.Fprintf(os.Stderr, "state dir already has objective %s; use a new --dir\n", existing.ID)
		return exitError
	}
	id := fmt.Sprintf("obj-%d", time.Now().UnixNano())
	t := *title
	if t == "" {
		t = firstLine(*text, 48)
	}
	obj := goal.Objective{ID: id, ProjectPath: *dir, Title: t, Goal: *text,
		Constraints: *constraints, ExpectedOutcome: *expected, Acceptance: *acceptance, Status: goal.StatusDraft}
	if err := st.CreateObjective(obj); err != nil {
		fmt.Fprintln(os.Stderr, "objective:", err)
		return exitError
	}
	_ = st.SetMeta("objective_name", t)
	_ = st.SetMeta("objective_hash", id)
	_ = st.SetMeta("policy_pack", "simple")
	fmt.Printf("objective=%s status=draft\n", id)
	return exitOK
}

func goalPlan(args []string) int {
	fs := flag.NewFlagSet("goal plan", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	which := fs.String("planner", "fake", "fake | model")
	planFile := fs.String("plan-file", "", "plan JSON for the fake planner")
	model := fs.String("model", "", "provider/model for the planner (default: settings or built-in)")
	executor := fs.String("executor", "opencode", "opencode | ao")
	maxTasks := fs.Int("max-tasks", 8, "task limit")
	projectContext := fs.String("context", "", "bounded project context for the planner")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	st, err := openGoalStore(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return exitError
	}
	defer st.Close()
	obj, ok, err := singleObjective(st)
	if err != nil || !ok {
		fmt.Fprintln(os.Stderr, "no objective in this state dir")
		return exitError
	}
	tasks, err := st.List()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tasks:", err)
		return exitError
	}
	if len(tasks) > 0 {
		fmt.Fprintln(os.Stderr, "tasks already exist for this objective; refusing to replace a plan that may have run")
		return exitError
	}

	var p planner.Planner
	if *which == "model" {
		plannerModel := resolveRoleModel(*dir, "planner", *model)
		fmt.Printf("planner model: %s\n", plannerModel)
		backend, closeBackend, err := openSessionBackend(*dir, *executor, plannerModel)
		if err != nil {
			fmt.Fprintln(os.Stderr, "backend:", err)
			return exitError
		}
		defer closeBackend()
		p = planner.NewModel(backend)
	} else {
		fake := &planner.Fake{}
		if *planFile != "" {
			raw, err := os.ReadFile(*planFile)
			if err != nil {
				fmt.Fprintln(os.Stderr, "plan file:", err)
				return exitError
			}
			var plan goal.Plan
			if err := json.Unmarshal(raw, &plan); err != nil {
				fmt.Fprintln(os.Stderr, "plan file:", err)
				return exitError
			}
			fake.Plans = []goal.Plan{plan}
		} else {
			fake.Plans = []goal.Plan{demoPlan()}
		}
		p = fake
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	plan, result, err := planner.Generate(ctx, p, planner.Request{Goal: obj.Goal, Constraints: obj.Constraints,
		Expected: obj.ExpectedOutcome, MaxTasks: *maxTasks, Context: *projectContext}, 2) // no host path: the model plans relative to the task workspace
	if err != nil {
		fmt.Fprintln(os.Stderr, "planner:", err)
		return exitError
	}
	hash, err := planner.Hash(plan)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hash:", err)
		return exitError
	}
	latest, ok, err := st.LatestPlan(obj.ID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plan:", err)
		return exitError
	}
	version := 1
	if ok {
		version = latest.Version + 1
	}
	if _, err := st.CreatePlanWithUsage(obj.ID, version, plan, hash, store.PlanUsage{
		PromptChars: result.PromptChars, OutputChars: result.OutputChars,
		PromptTokens: result.PromptTokens, OutputTokens: result.OutputTokens,
		UsageEstimated: result.UsageEstimated,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "save plan:", err)
		return exitError
	}
	if obj.Status == goal.StatusDraft {
		if err := st.MoveObjective(obj.ID, goal.StatusPlanned); err != nil {
			fmt.Fprintln(os.Stderr, "objective:", err)
			return exitError
		}
	}
	fmt.Printf("plan v%d ready for confirmation (%d tasks, hash %s)\n", version, len(plan.Tasks), hash[:12])
	for _, t := range plan.Tasks {
		fmt.Printf("  %-4s risk=%-6s deps=%-10s artifact=%s\n", t.ID, t.Risk, strings.Join(t.Deps, ","), t.Artifacts[0])
	}
	return exitOK
}

func goalShow(args []string) int {
	fs := flag.NewFlagSet("goal show", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	version := fs.Int("version", 0, "plan version (0 = latest)")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	st, err := openGoalStoreRead(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return exitError
	}
	defer st.Close()
	obj, ok, err := singleObjective(st)
	if err != nil || !ok {
		fmt.Fprintln(os.Stderr, "no objective in this state dir")
		return exitError
	}
	fmt.Printf("objective=%s status=%s plan=v%d\n", obj.ID, obj.Status, obj.PlanVersion)
	fmt.Printf("goal: %s\n", obj.Goal)
	var plan store.PlanRecord
	if *version > 0 {
		plan, ok, err = st.GetPlan(obj.ID, *version)
	} else {
		plan, ok, err = st.LatestPlan(obj.ID)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitError
	}
	if !ok {
		fmt.Println("no plan yet")
		return exitOK
	}
	fmt.Printf("plan v%d (%s) hash=%s\n", plan.Version, plan.Status, plan.Hash[:12])
	for _, t := range plan.Spec.SortedTasks() {
		fmt.Printf("  %-4s risk=%-6s deps=%-10s artifact=%-24s %s\n", t.ID, t.Risk,
			strings.Join(t.Deps, ","), t.Artifacts[0], t.Goal)
	}
	return exitOK
}

func goalConfirm(args []string) int {
	fs := flag.NewFlagSet("goal confirm", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	version := fs.Int("version", 0, "plan version (0 = latest)")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	st, err := openGoalStore(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return exitError
	}
	defer st.Close()
	obj, ok, err := singleObjective(st)
	if err != nil || !ok {
		fmt.Fprintln(os.Stderr, "no objective in this state dir")
		return exitError
	}
	v := *version
	if v == 0 {
		latest, ok, err := st.LatestPlan(obj.ID)
		if err != nil || !ok {
			fmt.Fprintln(os.Stderr, "no plan to confirm")
			return exitError
		}
		v = latest.Version
	}
	if err := st.ConfirmPlan(obj.ID, v); err != nil {
		fmt.Fprintln(os.Stderr, "confirm:", err)
		return exitError
	}
	fmt.Printf("plan v%d confirmed; objective is active\n", v)
	return exitOK
}

// approvalReasons lists the actions a human should see before a plan runs. It
// is deliberately conservative: high-risk tasks and every command validator
// need an explicit approval, because command checks execute with the current
// user's permissions.
func approvalReasons(p goal.Plan) []string {
	var out []string
	for _, t := range p.Tasks {
		if strings.EqualFold(strings.TrimSpace(t.Risk), "high") {
			out = append(out, fmt.Sprintf("%s is marked high risk", t.ID))
		}
		for _, v := range t.Validators {
			if v.Type == "command" {
				out = append(out, fmt.Sprintf("%s runs: %s", t.ID, v.Command))
			}
		}
	}
	return out
}

func goalRun(args []string) int {
	fs := flag.NewFlagSet("goal run", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	reviewerOn := fs.Bool("reviewer", false, "use the agent reviewer")
	maxRuns := fs.Int("max-runs", 0, "maximum runs")
	maxReviews := fs.Int("max-reviews", 0, "maximum review sessions")
	maxTokens := fs.Int("max-tokens", 0, "estimated token budget")
	runTimeout := fs.Duration("run-timeout", 20*time.Minute, "per-run timeout")
	limit := fs.Duration("timeout", 45*time.Minute, "overall budget")
	interval := fs.Duration("interval", 3*time.Second, "poll interval")
	executor := fs.String("executor", "opencode", "opencode | ao | fake")
	model := fs.String("model", "", "provider/model for planner and workers (default: settings or built-in)")
	reviewModel := fs.String("review-model", "", "provider/model for the reviewer (default: --model)")
	acceptanceModel := fs.String("acceptance-model", "", "provider/model for acceptance (default: --model)")
	acceptanceOn := fs.Bool("acceptance", true, "run the acceptance judge after the reviewer")
	clearQuarantine := fs.Bool("clear-quarantine", false, "clear a persisted quarantine before running")
	approveRisk := fs.Bool("approve-risk", false, "approve high-risk tasks and command validators before running")
	fakeFail := fs.String("fake-fail", "", "comma-separated task ids to fail (fake executor only)")
	fakeOutput := fs.String("fake-output", "", "task:attempt=text overrides for the fake executor")
	fakeTicks := fs.Int("fake-ticks", 1, "observations before a fake run completes")
	recoverOn := fs.Bool("recover", true, "bounded automatic recovery after a blocked task")
	maxRecoveries := fs.Int("max-recoveries", 2, "maximum automatic recovery actions per objective")
	recoveryModel := fs.String("recovery-model", "", "optional provider/model for one bounded unknown-failure diagnosis")
	replanOn := fs.Bool("replan", false, "allow one bounded replan after a failure")
	maxReplans := fs.Int("max-replans", 1, "maximum replans per run")
	replanPlan := fs.String("replan-plan-file", "", "replacement plan JSON for the fake replanner")
	replanPlanner := fs.String("replan-planner", "model", "model | fake")
	keepWork := fs.Int("keep-work", 20, "keep the newest N work directories (0 = keep all)")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	// The one-replan policy is a hard invariant, not a tunable.
	if *maxReplans > replan.MaxReplans {
		*maxReplans = replan.MaxReplans
	}
	if *maxRecoveries < 0 {
		*maxRecoveries = 0
	}
	if strings.TrimSpace(*dir) == "" {
		fmt.Fprintln(os.Stderr, "run requires --dir")
		return exitError
	}
	// store.Open takes the state-dir lock, so one scheduler owns a state dir and
	// `goal cancel` can tell whether anyone is listening for its request.
	st, err := openGoalStore(*dir)
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			fmt.Fprintf(os.Stderr, "another scheduler owns %s; run `kernel goal cancel --dir %s` first\n", *dir, *dir)
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
	if obj.Status != goal.StatusActive {
		fmt.Fprintf(os.Stderr, "objective is %s; confirm a plan first\n", obj.Status)
		return exitError
	}
	plan, ok, err := st.GetPlan(obj.ID, obj.PlanVersion)
	if err != nil || !ok {
		fmt.Fprintln(os.Stderr, "confirmed plan not found")
		return exitError
	}
	_ = st.SetMeta("recovery_budget", strconv.Itoa(*maxRecoveries))
	if err := compile.ValidatePlanForKernel(plan.Spec); err != nil {
		fmt.Fprintln(os.Stderr, "plan invalid:", err)
		return exitError
	}
	if reasons := approvalReasons(plan.Spec); len(reasons) > 0 && !*approveRisk {
		fmt.Fprintln(os.Stderr, "this plan needs explicit approval before it can run:")
		for _, reason := range reasons {
			fmt.Fprintln(os.Stderr, "  - "+reason)
		}
		fmt.Fprintln(os.Stderr, "review the actions, then rerun with --approve-risk")
		return exitBlocked
	}

	workerModel := resolveRoleModel(*dir, "worker", *model)
	settings := modelcfg.Load(*dir)
	reviewerModel := firstNonEmpty(*reviewModel, settings.Reviewer, workerModel)
	acceptanceModelResolved := firstNonEmpty(*acceptanceModel, settings.Acceptance, workerModel)
	fmt.Printf("models: worker=%s reviewer=%s acceptance=%s\n", workerModel, reviewerModel, acceptanceModelResolved)

	var backend exec.Backend
	var env *aoenv.Env
	var projectID string
	cleanup := func() {}
	if *executor == "fake" {
		fake := exec.NewFake(*fakeTicks)
		fillFakeOutputs(fake, plan.Spec)
		for _, id := range strings.Split(*fakeFail, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				fake.Fail[domain.TaskID(id)] = true
			}
		}
		if strings.TrimSpace(*fakeOutput) != "" {
			fake.ScriptedOutput = map[domain.TaskID]map[int]string{}
			for _, item := range strings.Split(*fakeOutput, ",") {
				key, value, ok := strings.Cut(strings.TrimSpace(item), "=")
				if !ok {
					fmt.Fprintln(os.Stderr, "fake-output must be task:attempt=text")
					return exitError
				}
				task, attemptText, ok := strings.Cut(key, ":")
				attempt, err := strconv.Atoi(attemptText)
				if !ok || err != nil {
					fmt.Fprintln(os.Stderr, "fake-output must be task:attempt=text")
					return exitError
				}
				if fake.ScriptedOutput[domain.TaskID(task)] == nil {
					fake.ScriptedOutput[domain.TaskID(task)] = map[int]string{}
				}
				fake.ScriptedOutput[domain.TaskID(task)][attempt] = value
			}
		}
		backend = fake
	} else if *executor == "opencode" {
		backend = opencodecli.New(opencodecli.Config{
			Model: workerModel, Bin: opencodeBin(), BaseDir: filepath.Join(*dir, "repo"), WorkRoot: filepath.Join(*dir, "work"),
		})
	} else {
		env, err = aoenv.Setup(*dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ao env:", err)
			return exitError
		}
		cleanup = env.Close
		projectID, err = env.CreateProject()
		if err != nil {
			cleanup()
			fmt.Fprintln(os.Stderr, "project:", err)
			return exitError
		}
		backend = aoBackend(env, projectID, workerModel)
	}
	defer cleanup()
	validators := compile.Validators(plan.Spec)
	e := engine.New(st, backend, 2, validators).
		WithStateDir(*dir).
		WithBudget(engine.Budget{MaxRuns: *maxRuns, MaxReviews: *maxReviews, MaxTokens: *maxTokens}).
		WithRunTimeout(*runTimeout)
	if *clearQuarantine {
		if err := e.ClearQuarantine(); err != nil {
			fmt.Fprintln(os.Stderr, "clear quarantine:", err)
			return exitError
		}
	} else if reason := e.RestoreQuarantine(); reason != "" {
		fmt.Fprintf(os.Stderr, "objective is quarantined: %s\n", reason)
		fmt.Fprintln(os.Stderr, "inspect the workspace, then rerun with --clear-quarantine")
		return exitBlocked
	}
	// Cancel and pause flags from a previous scheduler are stale: they were
	// written while that scheduler owned the dir, and we own it now.
	_ = os.Remove(cancelPath(*dir))
	_ = os.Remove(pausePath(*dir))
	// Make accepted artifacts from sibling objectives available as a read-only
	// project namespace. Failure here is a context convenience, not a reason to
	// block the objective.
	_ = seedProjectArtifacts(*dir)
	// Role backends share the workspace root; for AO they also share the daemon.
	roleBackend := func(model string) exec.Backend {
		if *executor == "opencode" {
			return opencodecli.New(opencodecli.Config{
				Model: model, Bin: opencodeBin(), BaseDir: filepath.Join(*dir, "repo"), WorkRoot: filepath.Join(*dir, "work"),
			})
		}
		return aoBackend(env, projectID, model)
	}
	if *reviewerOn {
		if *executor == "fake" {
			fmt.Fprintln(os.Stderr, "--reviewer needs a real executor")
			return exitError
		}
		e = e.WithReviewer(ao.NewReviewer(roleBackend(reviewerModel)))
	}
	if *acceptanceOn {
		if *executor != "fake" {
			acc := ao.NewAcceptance(roleBackend(acceptanceModelResolved))
			// Task acceptance answers "did this artifact do its job"; objective
			// acceptance answers "did the delivered set satisfy the original goal".
			e = e.WithAcceptance(acc).WithObjectiveJudge(acc)
		}
	}
	existing := map[domain.TaskID]bool{}
	if tasks, err := st.List(); err == nil {
		for _, t := range tasks {
			existing[t.ID] = true
		}
	}
	for _, task := range compile.Tasks(plan.Spec, obj.ID, obj.PlanVersion) {
		if existing[task.ID] {
			continue
		}
		if err := e.Add(task); err != nil {
			fmt.Fprintln(os.Stderr, "add task:", err)
			return exitError
		}
		existing[task.ID] = true
	}

	ctx, cancel := context.WithTimeout(context.Background(), *limit)
	defer cancel()
	replans := 0
	paused := false
	needsApproval := len(approvalReasons(plan.Spec)) > 0
	for {
		if reason, cancelled := takeCancelRequest(*dir); cancelled {
			abortCtx, abortCancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, aerr := e.Abort(abortCtx, "cancelled: "+reason)
			abortCancel()
			if aerr != nil {
				fmt.Fprintln(os.Stderr, "cancel:", aerr)
			}
			_, _ = st.BlockPending("cancelled: " + reason)
			if err := st.MoveObjective(obj.ID, goal.StatusCancelled); err != nil {
				fmt.Fprintln(os.Stderr, "objective:", err)
			}
			_ = st.AppendEvent("", "objective_cancelled", reason)
			_, _ = pruneWork(*dir, *keepWork)
			fmt.Println("objective cancelled")
			return exitBlocked
		}
		if pauseRequested(*dir) {
			if !paused {
				fmt.Println("scheduler paused; waiting for resume")
				paused = true
			}
			select {
			case <-ctx.Done():
				abortCtx, abortCancel := context.WithTimeout(context.Background(), 30*time.Second)
				_, _ = e.Abort(abortCtx, "goal timeout while paused")
				abortCancel()
				fmt.Fprintln(os.Stderr, "goal timed out while paused")
				return exitError
			case <-time.After(*interval):
			}
			continue
		}
		if paused {
			fmt.Println("scheduler resumed")
			paused = false
		}
		if _, err := e.Step(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "step:", err)
			return exitError
		}
		all, err := st.List()
		if err != nil {
			fmt.Fprintln(os.Stderr, "list:", err)
			return exitError
		}
		if !terminal(all) {
			select {
			case <-ctx.Done():
				abortCtx, abortCancel := context.WithTimeout(context.Background(), 30*time.Second)
				_, _ = e.Abort(abortCtx, "goal timeout")
				abortCancel()
				_, _ = pruneWork(*dir, *keepWork)
				fmt.Fprintln(os.Stderr, "goal timed out")
				return exitError
			case <-time.After(*interval):
			}
			continue
		}

		current, err := st.TasksForPlan(obj.ID, obj.PlanVersion)
		if err != nil {
			fmt.Fprintln(os.Stderr, "plan tasks:", err)
			return exitError
		}
		accepted := true
		for _, t := range current {
			if t.Status != domain.TaskAccepted {
				accepted = false
			}
		}
		if accepted {
			blocked, jerr := e.JudgeObjective(ctx, obj, plan.Spec)
			if jerr != nil {
				fmt.Fprintln(os.Stderr, "objective acceptance:", jerr)
				_ = st.AppendEvent("", "objective_acceptance_error", jerr.Error())
				return failGoal(st, obj, current)
			}
			if blocked {
				fmt.Fprintln(os.Stderr, "objective acceptance: the delivered set does not fulfil the original goal")
				_ = st.AppendEvent("", "objective_blocked", "objective acceptance found a blocking gap")
				return failGoal(st, obj, current)
			}
			code := finishGoal(st, obj, current, *dir)
			_, _ = pruneWork(*dir, *keepWork)
			return code
		}
		if *recoverOn {
			policy := recovery.Policy{MaxRecoveries: *maxRecoveries, AttemptCap: 3, ReplansLeft: *maxReplans - replans}
			decision, derr := recovery.DecideWithSuggestion(st, obj, needsApproval, policy, nil)
			if derr != nil {
				fmt.Fprintln(os.Stderr, "recovery:", derr)
				return exitError
			}
			if *recoveryModel != "" && decision.Action == "stop" && decision.Category == "unknown" {
				if calls, _ := recovery.ModelCalls(st); calls < 1 {
					if _, cerr := recovery.IncrementModelCalls(st); cerr != nil {
						fmt.Fprintln(os.Stderr, "recovery model calls:", cerr)
						return exitError
					}
					diagBackend, closeDiag, berr := openSessionBackend(*dir, *executor, *recoveryModel)
					if berr != nil {
						_ = st.AppendEvent("", "recovery_model_error", berr.Error())
						fmt.Fprintln(os.Stderr, "recovery diagnosis backend:", berr)
					} else {
						model := &recovery.Model{Backend: diagBackend, Timeout: 90 * time.Second, Poll: time.Second}
						suggestion, serr := model.Diagnose(ctx, strings.Join(decision.Evidence, "\n"))
						closeDiag()
						if serr != nil {
							_ = st.AppendEvent("", "recovery_model_error", serr.Error())
							fmt.Fprintln(os.Stderr, "recovery diagnosis:", serr)
						} else {
							_ = st.AppendEvent("", "recovery_model_suggestion", suggestion.Category+": "+suggestion.Reason)
							if next, nerr := recovery.DecideWithSuggestion(st, obj, needsApproval, policy, &suggestion); nerr == nil {
								decision = next
							}
						}
					}
				}
			}
			switch decision.Action {
			case "retry":
				count, cerr := recovery.Increment(st)
				if cerr != nil {
					fmt.Fprintln(os.Stderr, "recovery budget:", cerr)
					return exitError
				}
				ok, rerr := st.RequeueBlocked(domain.TaskID(decision.TaskID), "recovery: "+decision.Reason, 3)
				if rerr != nil || !ok {
					fmt.Fprintf(os.Stderr, "recovery requeue %s: ok=%v err=%v\n", decision.TaskID, ok, rerr)
					break
				}
				_ = recovery.ClearPending(st)
				_ = st.AppendEvent(domain.TaskID(decision.TaskID), "recovery_retry",
					fmt.Sprintf("%s (count %d/%d)", decision.Reason, count, *maxRecoveries))
				fmt.Printf("recovery: retrying %s (%s) count=%d/%d\n", decision.TaskID, decision.Reason, count, *maxRecoveries)
				continue
			case "approve":
				if err := recovery.SetPending(st, decision); err != nil {
					fmt.Fprintln(os.Stderr, "recovery pending:", err)
					return exitError
				}
				_ = st.AppendEvent("", "recovery_approval_required", decision.Reason)
				fmt.Printf("recovery: approval required for %s (%s)\n", decision.TaskID, decision.Reason)
				fmt.Printf("review it, then run: kernel goal recover --dir %s --approve\n", *dir)
				_, _ = pruneWork(*dir, *keepWork)
				return exitBlocked
			case "replan":
				// Fall through to the existing bounded replanner below.
			default:
				if decision.Category == "unknown_execution_state" || decision.Category == "unexpected_side_effect" {
					if err := recovery.SetPending(st, decision); err != nil {
						fmt.Fprintln(os.Stderr, "recovery pending:", err)
						return exitError
					}
					_ = st.AppendEvent("", "recovery_approval_required", "outcome unknown; inspect before requeue")
					fmt.Printf("recovery: outcome unknown for %s; refusing to retry automatically\n", decision.TaskID)
					fmt.Printf("after checking side effects, run: kernel goal recover --dir %s --approve --task %s\n", *dir, decision.TaskID)
					_, _ = pruneWork(*dir, *keepWork)
					return exitBlocked
				}
				code := failGoal(st, obj, current)
				_, _ = pruneWork(*dir, *keepWork)
				return code
			}
		}
		if !*replanOn || replans >= *maxReplans {
			code := failGoal(st, obj, current)
			_, _ = pruneWork(*dir, *keepWork)
			return code
		}

		// Bounded replan: keep accepted tasks, add a small fix set.
		record, ok, err := st.GetPlan(obj.ID, obj.PlanVersion)
		if err != nil || !ok {
			fmt.Fprintln(os.Stderr, "current plan not found")
			return exitError
		}
		feedback, _, acceptedIDs, err := replan.FailureContext(st, record.Spec)
		if err != nil {
			fmt.Fprintln(os.Stderr, "replan context:", err)
			return exitError
		}
		var rp planner.Planner
		if *replanPlanner == "fake" {
			raw, err := os.ReadFile(*replanPlan)
			if err != nil {
				fmt.Fprintln(os.Stderr, "replan plan file:", err)
				return exitError
			}
			var p goal.Plan
			if err := json.Unmarshal(raw, &p); err != nil {
				fmt.Fprintln(os.Stderr, "replan plan file:", err)
				return exitError
			}
			rp = &planner.Fake{Plans: []goal.Plan{p}}
		} else {
			if *executor == "fake" {
				fmt.Fprintln(os.Stderr, "--replan-planner model needs a real executor")
				return exitError
			}
			rp = planner.NewModel(backend)
		}
		var newPlan goal.Plan
		replanErr := error(nil)
		for attempt := 0; attempt < 2; attempt++ {
			planAttempt, _, err := planner.Generate(ctx, rp, planner.Request{
				Goal: obj.Goal, Constraints: obj.Constraints, Feedback: feedback,
				MaxTasks: len(record.Spec.Tasks) + replan.MaxNewTasks,
			}, 2)
			if err != nil {
				replanErr = err
				break
			}
			if err := replan.ValidateReplacement(record.Spec, planAttempt, acceptedIDs); err != nil {
				replanErr = err
				feedback += "\nThe replacement was rejected: " + err.Error() +
					"\nRetired tasks must NOT be reused; create new ids such as T1-fix."
				rp = freshReplanner(*replanPlanner, *replanPlan, backend)
				continue
			}
			// Validation only sees the previous plan. A task id retired two plans
			// ago still exists in the database and cannot be reused either.
			if err := rejectHistoricalID(planAttempt, record.Spec, existing); err != nil {
				replanErr = err
				feedback += "\nThe replacement was rejected: " + err.Error()
				rp = freshReplanner(*replanPlanner, *replanPlan, backend)
				continue
			}
			newPlan = planAttempt
			replanErr = nil
			break
		}
		if replanErr != nil {
			fmt.Fprintln(os.Stderr, "replan rejected:", replanErr)
			return failGoal(st, obj, current)
		}
		hash, err := planner.Hash(newPlan)
		if err != nil {
			fmt.Fprintln(os.Stderr, "hash:", err)
			return exitError
		}
		version := obj.PlanVersion + 1
		retired, err := replan.Apply(st, obj.ID, version, record.Spec, newPlan, hash)
		if err != nil {
			fmt.Fprintln(os.Stderr, "replan apply:", err)
			return exitError
		}
		obj.PlanVersion = version
		replans++
		for name, check := range compile.Validators(newPlan) {
			validators[name] = check
		}
		if fake, ok := backend.(*exec.FakeBackend); ok {
			fillFakeOutputs(fake, newPlan)
		}
		fmt.Printf("replanned to v%d (%d tasks retired)\n", version, retired)
		for _, task := range compile.Tasks(newPlan, obj.ID, version) {
			if existing[task.ID] {
				continue
			}
			if err := e.Add(task); err != nil {
				fmt.Fprintln(os.Stderr, "add task:", err)
				return exitError
			}
			existing[task.ID] = true
		}
	}
}

func finishGoal(st *store.Store, obj goal.Objective, tasks []domain.Task, dir string) int {
	for _, t := range tasks {
		fmt.Printf("task %-4s %-9s attempts=%d %s\n", t.ID, t.Status, t.Attempts, t.LastError)
	}
	files, aerr := delivery.Assemble(st, dir)
	if aerr != nil {
		fmt.Fprintln(os.Stderr, "assemble:", aerr)
	} else {
		fmt.Printf("artifacts assembled in %s/out (%d files)\n", dir, files)
	}
	checks, cerr := delivery.VerifyAssembled(st, dir)
	if cerr != nil {
		fmt.Fprintln(os.Stderr, "delivery verify:", cerr)
	}
	var failed []delivery.Check
	for _, c := range checks {
		if !c.OK {
			failed = append(failed, c)
		}
	}
	broken := aerr != nil || cerr != nil || len(failed) > 0
	status := goal.StatusCompleted
	if broken {
		status = goal.StatusFailed
	}

	// Write the delivery artifacts before flipping the objective status. If any of
	// them fails, the objective stays active and a rerun can fix it; claiming
	// completed first would block that rerun.
	obj.Status = status
	report, err := delivery.Build(st, obj)
	if err != nil {
		fmt.Fprintln(os.Stderr, "delivery report:", err)
		return exitError
	}
	report.Checks = checks
	if err := delivery.Write(dir, report); err != nil {
		fmt.Fprintln(os.Stderr, "delivery:", err)
		return exitError
	}
	fmt.Printf("delivery written to %s/delivery.md\n", dir)

	if !broken {
		snap, err := projectstate.Build(st, obj, dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "snapshot:", err)
			return exitError
		}
		if err := projectstate.Write(dir, snap); err != nil {
			fmt.Fprintln(os.Stderr, "snapshot:", err)
			return exitError
		}
	}

	if err := st.MoveObjective(obj.ID, status); err != nil {
		fmt.Fprintln(os.Stderr, "objective:", err)
		fmt.Fprintln(os.Stderr, "completion was not recorded; refusing to report success")
		return exitError
	}

	// Publish only after the completion fact is durable. A publish failure is
	// reported but must not rewrite an already completed objective.
	if !broken {
		if err := publishProjectArtifactsForDir(dir); err != nil {
			fmt.Fprintln(os.Stderr, "shared artifacts:", err)
			_ = st.AppendEvent("", "artifact_publish_error", err.Error())
		}
	}

	if broken {
		for _, c := range failed {
			fmt.Printf("delivery check failed: %s `%s`: %s\n", c.TaskID, c.Command, c.Message)
		}
		if aerr != nil || cerr != nil {
			fmt.Println("objective failed: the delivery could not be assembled or verified")
		} else {
			fmt.Println("objective failed: the assembled delivery did not pass its own checks")
		}
		return exitBlocked
	}
	fmt.Printf("objective completed (%d delivery checks passed)\n", len(checks))
	return exitOK
}

func freshReplanner(kind, planFile string, backend exec.Backend) planner.Planner {
	if kind == "fake" {
		raw, err := os.ReadFile(planFile)
		if err != nil {
			return &planner.Fake{}
		}
		var p goal.Plan
		if err := json.Unmarshal(raw, &p); err != nil {
			return &planner.Fake{}
		}
		return &planner.Fake{Plans: []goal.Plan{p}}
	}
	return planner.NewModel(backend)
}

func rejectHistoricalID(next, old goal.Plan, existing map[domain.TaskID]bool) error {
	current := map[string]bool{}
	for _, t := range old.Tasks {
		current[t.ID] = true
	}
	for _, t := range next.Tasks {
		if !current[t.ID] && existing[domain.TaskID(t.ID)] {
			return fmt.Errorf("task id %s already exists from an earlier plan; use a new id", t.ID)
		}
	}
	return nil
}

func failGoal(st *store.Store, obj goal.Objective, tasks []domain.Task) int {
	for _, t := range tasks {
		fmt.Printf("task %-4s %-9s attempts=%d %s\n", t.ID, t.Status, t.Attempts, t.LastError)
	}
	_ = st.MoveObjective(obj.ID, goal.StatusFailed)
	fmt.Println("objective failed; see blocked reasons above")
	return exitBlocked
}

func goalProgress(args []string) int {
	fs := flag.NewFlagSet("goal progress", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	st, err := openGoalStoreRead(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return exitError
	}
	defer st.Close()
	obj, ok, err := singleObjective(st)
	if err != nil || !ok {
		fmt.Fprintln(os.Stderr, "no objective in this state dir")
		return exitError
	}
	plan, ok, err := st.GetPlan(obj.ID, obj.PlanVersion)
	if err != nil || !ok {
		fmt.Println("no confirmed plan")
		return exitOK
	}
	r, err := progress.Assess(st, obj, len(approvalReasons(plan.Spec)) > 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "progress:", err)
		return exitError
	}
	fmt.Println(r)
	if r.FailureClass != "" {
		fmt.Printf("failure=%s action=%s\n", r.FailureClass, r.SuggestedAction)
	}
	return exitOK
}

func goalMetrics(args []string) int {
	fs := flag.NewFlagSet("goal metrics", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	st, err := openGoalStoreRead(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return exitError
	}
	defer st.Close()
	m, err := st.Metrics()
	if err != nil {
		fmt.Fprintln(os.Stderr, "metrics:", err)
		return exitError
	}
	raw, _ := json.MarshalIndent(m, "", "  ")
	fmt.Println(string(raw))
	return exitOK
}

func goalStatus(args []string) int {
	fs := flag.NewFlagSet("goal status", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	st, err := openGoalStoreRead(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return exitError
	}
	defer st.Close()
	obj, ok, err := singleObjective(st)
	if err != nil || !ok {
		fmt.Fprintln(os.Stderr, "no objective in this state dir")
		return exitError
	}
	fmt.Printf("objective=%s status=%s plan=v%d\n", obj.ID, obj.Status, obj.PlanVersion)
	plans, _ := st.ListPlans(obj.ID)
	for _, p := range plans {
		fmt.Printf("  plan v%d %s\n", p.Version, p.Status)
	}
	tasks, _ := st.List()
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	for _, t := range tasks {
		fmt.Printf("  %-4s %-9s attempts=%d %s\n", t.ID, t.Status, t.Attempts, t.LastError)
	}
	return exitOK
}

// fillFakeOutputs makes the deterministic executor produce exactly what each
// validator expects; it is only used by --executor fake.
func fillFakeOutputs(fake *exec.FakeBackend, p goal.Plan) {
	for _, t := range p.Tasks {
		if len(t.Validators) == 0 {
			continue
		}
		switch t.Validators[0].Type {
		case "exact", "contains":
			fake.Output[domain.TaskID(t.ID)] = t.Validators[0].Expect
		default:
			fake.Output[domain.TaskID(t.ID)] = "ok"
		}
	}
}

func goalExport(args []string) int {
	fs := flag.NewFlagSet("goal export", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	st, err := openGoalStoreRead(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return exitError
	}
	defer st.Close()
	obj, ok, err := singleObjective(st)
	if err != nil || !ok {
		fmt.Fprintln(os.Stderr, "no objective in this state dir")
		return exitError
	}
	snap, err := projectstate.Build(st, obj, *dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "snapshot:", err)
		return exitError
	}
	if err := projectstate.Write(*dir, snap); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		return exitError
	}
	fmt.Printf("snapshot written to %s/.project (hash %s)\n", *dir, snap.Hash[:12])
	return exitOK
}

func goalResume(args []string) int {
	fs := flag.NewFlagSet("goal resume", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	write := fs.Bool("write", false, "regenerate the .project snapshot from the database")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	st, err := openGoalStoreRead(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return exitError
	}
	defer st.Close()
	obj, ok, err := singleObjective(st)
	if err != nil || !ok {
		fmt.Fprintln(os.Stderr, "no objective in this state dir; runtime state is authoritative and must exist to resume")
		return exitError
	}
	stored, hasSnapshot, err := projectstate.Read(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "snapshot:", err)
		return exitError
	}
	if !hasSnapshot {
		fmt.Println("no .project snapshot; run `kernel goal export` to create one")
	} else if drift, err := projectstate.Drift(st, obj, *dir); err != nil {
		fmt.Fprintln(os.Stderr, "drift:", err)
		return exitError
	} else if drift {
		fmt.Println("snapshot is stale (database changed since export)")
	} else {
		fmt.Printf("snapshot matches database (generated %s)\n", stored.GeneratedAt.Format(time.RFC3339))
	}
	if *write {
		snap, err := projectstate.Build(st, obj, *dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "snapshot:", err)
			return exitError
		}
		if err := projectstate.Write(*dir, snap); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			return exitError
		}
		fmt.Println("snapshot regenerated")
	}
	fmt.Printf("objective=%s status=%s plan=v%d\n", obj.ID, obj.Status, obj.PlanVersion)
	tasks, _ := st.List()
	open := 0
	for _, t := range tasks {
		if t.Status != domain.TaskAccepted && t.Status != domain.TaskSuperseded {
			open++
		}
	}
	fmt.Printf("open tasks=%d\n", open)
	if obj.Status == goal.StatusActive && open > 0 {
		fmt.Println("next: kernel goal run --dir " + *dir)
	} else if obj.Status == goal.StatusCompleted {
		fmt.Println("next: review delivery.md")
	} else if obj.Status == goal.StatusFailed {
		fmt.Println("next: inspect blocked tasks, then rerun with --replan")
	}
	return exitOK
}

func demoPlan() goal.Plan {
	return goal.Plan{
		ObjectiveAcceptance: []string{"a.txt contains hello from planner"},
		Tasks: []goal.PlanTask{{
			ID: "T1", Goal: "create a.txt with the exact text hello from planner",
			Artifacts: []string{"a.txt"}, Risk: "low",
			Acceptance: []string{"a.txt equals hello from planner"},
			Validators: []verify.Spec{{Type: "exact", Expect: "hello from planner"}},
		}},
	}
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// Cancelling is a request, not a write: only the scheduler that owns the state
// dir may change task state, so callers drop a flag file that the scheduler
// honours on its next iteration.
func cancelPath(dir string) string { return filepath.Join(dir, "cancel") }

func pausePath(dir string) string { return filepath.Join(dir, "pause") }

func pauseRequested(dir string) bool { return fileExists(pausePath(dir)) }

// Pause is a request to a live scheduler; it is not an Objective status. The
// current in-flight Run is allowed to finish, but no new task is started.
func goalPause(args []string) int {
	fs := flag.NewFlagSet("goal pause", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	reason := fs.String("reason", "paused by user", "why scheduling is paused")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if strings.TrimSpace(*dir) == "" {
		fmt.Fprintln(os.Stderr, "pause requires --dir")
		return exitError
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "pause:", err)
		return exitError
	}
	// If no scheduler owns the lock, there is nothing to pause; do not leave a
	// stale flag that would surprise the next run.
	st, err := openGoalStore(*dir)
	if err == nil {
		st.Close()
		fmt.Println("no scheduler is running; pause applies only to a live scheduler")
		return exitOK
	}
	if !errors.Is(err, store.ErrLocked) {
		fmt.Fprintln(os.Stderr, "pause:", err)
		return exitError
	}
	if err := os.WriteFile(pausePath(*dir), []byte(*reason+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "pause:", err)
		return exitError
	}
	fmt.Println("pause requested; the scheduler will stop starting new tasks")
	return exitOK
}

func goalUnpause(args []string) int {
	fs := flag.NewFlagSet("goal unpause", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if strings.TrimSpace(*dir) == "" {
		fmt.Fprintln(os.Stderr, "resume requires --dir")
		return exitError
	}
	if err := os.Remove(pausePath(*dir)); err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "resume:", err)
		return exitError
	}
	fmt.Println("scheduler resumed")
	return exitOK
}

func cancelRequested(dir string) bool {
	_, err := os.Stat(cancelPath(dir))
	return err == nil
}

func takeCancelRequest(dir string) (string, bool) {
	raw, err := os.ReadFile(cancelPath(dir))
	if err != nil {
		return "", false
	}
	_ = os.Remove(cancelPath(dir))
	reason := strings.TrimSpace(string(raw))
	if reason == "" {
		reason = "cancelled by user"
	}
	return reason, true
}

func goalCancel(args []string) int {
	fs := flag.NewFlagSet("goal cancel", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	reason := fs.String("reason", "cancelled by user", "why the objective is being cancelled")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if strings.TrimSpace(*dir) == "" {
		fmt.Fprintln(os.Stderr, "cancel requires --dir")
		return exitError
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "cancel:", err)
		return exitError
	}
	if _, err := os.Stat(filepath.Join(*dir, "kernel.db")); err != nil {
		fmt.Println("no objective in this state dir")
		return exitOK
	}
	// store.Open takes the state-dir lock. If it succeeds we own the directory and
	// can cancel in-process; there is no window where the lock is dropped between
	// deciding and acting. If it fails, a scheduler owns it and will consume the
	// flag we leave behind.
	st, err := openGoalStore(*dir)
	if err != nil {
		if !errors.Is(err, store.ErrLocked) {
			fmt.Fprintln(os.Stderr, "cancel:", err)
			return exitError
		}
		if werr := os.WriteFile(cancelPath(*dir), []byte(*reason+"\n"), 0o644); werr != nil {
			fmt.Fprintln(os.Stderr, "cancel:", werr)
			return exitError
		}
		fmt.Println("cancel requested; the running scheduler will stop")
		return exitOK
	}
	defer st.Close()
	_ = os.Remove(cancelPath(*dir))
	return cancelObjective(st, *dir, *reason)
}

func cancelObjective(st *store.Store, dir, reason string) int {
	obj, ok, err := singleObjective(st)
	if err != nil {
		fmt.Fprintln(os.Stderr, "objective:", err)
		return exitError
	}
	if !ok {
		fmt.Println("no objective in this state dir")
		return exitOK
	}
	// A finished objective keeps its outcome: cancelling must not relabel a
	// negative result as an intentional stop.
	switch obj.Status {
	case goal.StatusCancelled:
		fmt.Printf("objective %s is already cancelled\n", obj.ID)
		return exitOK
	case goal.StatusCompleted, goal.StatusFailed:
		fmt.Printf("objective %s already finished as %s; cancel does not rewrite outcomes\n", obj.ID, obj.Status)
		return exitBlocked
	}
	if _, err := st.AbandonRunning(reason, nil); err != nil {
		fmt.Fprintln(os.Stderr, "cancel:", err)
		return exitError
	}
	if _, err := st.BlockPending(reason); err != nil {
		fmt.Fprintln(os.Stderr, "cancel:", err)
		return exitError
	}
	if err := st.MoveObjective(obj.ID, goal.StatusCancelled); err != nil {
		fmt.Fprintln(os.Stderr, "objective:", err)
		return exitError
	}
	_ = st.AppendEvent("", "objective_cancelled", reason)
	fmt.Printf("objective %s cancelled\n", obj.ID)
	return exitOK
}

// goalReport prints a plain-language briefing. It changes no state; the model
// variant only rewrites the text.
func goalReport(args []string) int {
	fs := flag.NewFlagSet("goal report", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	llm := fs.Bool("llm", false, "rewrite the briefing with the model")
	model := fs.String("model", "", "provider/model for the report (default: settings or built-in)")
	executor := fs.String("executor", "opencode", "opencode | ao")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	st, err := openGoalStoreRead(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return exitError
	}
	obj, ok, err := singleObjective(st)
	if err != nil || !ok {
		st.Close()
		fmt.Fprintln(os.Stderr, "no objective in this state dir")
		return exitError
	}
	brief, err := report.Build(st, obj)
	tasks, _ := st.List()
	stateHash := report.StateHash(obj, tasks)
	st.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "report:", err)
		return exitError
	}
	text := report.Render(brief)
	if *llm {
		// A briefing must never interrupt a run: Setup reaps leftover sessions,
		// which is only safe when no scheduler owns the directory.
		lock, lerr := store.AcquireLock(filepath.Join(*dir, "kernel.lock"))
		if lerr != nil {
			fmt.Fprintln(os.Stderr, "a run owns this state dir; wait for it to finish or use the rule-based summary")
			return exitError
		}
		lock.Release()
		backend, closeBackend, err := openSessionBackend(*dir, *executor, resolveRoleModel(*dir, "reporter", *model))
		if err != nil {
			fmt.Fprintln(os.Stderr, "backend:", err)
			return exitError
		}
		defer closeBackend()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		facts := text
		if files, ferr := delivery.List(*dir, 20, 600); ferr == nil && len(files) > 0 {
			facts += "\n\n交付文件：\n" + delivery.RenderDeliverables(files)
		}
		polished, perr := report.Polish(ctx, backendSummarizer{backend}, facts)
		if perr != nil {
			fmt.Fprintln(os.Stderr, "polish:", perr)
			return exitError
		}
		text = polished
		if werr := report.WriteModelReport(*dir, stateHash, text); werr != nil {
			fmt.Fprintln(os.Stderr, "write report:", werr)
		}
	}
	fmt.Println(text)
	return exitOK
}

// goalExplain translates one task's review findings into Chinese. It caches by
// the findings hash, so re-running costs nothing until the findings change.
func goalExplain(args []string) int {
	fs := flag.NewFlagSet("goal explain", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	taskID := fs.String("task", "", "task id")
	model := fs.String("model", "", "provider/model for the explanation (default: settings or built-in)")
	executor := fs.String("executor", "opencode", "opencode | ao")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if strings.TrimSpace(*taskID) == "" {
		fmt.Fprintln(os.Stderr, "explain requires --task")
		return exitError
	}
	st, err := openGoalStoreRead(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return exitError
	}
	task, ok, err := st.Get(domain.TaskID(*taskID))
	if err != nil || !ok {
		st.Close()
		fmt.Fprintln(os.Stderr, "unknown task")
		return exitError
	}
	reviews, err := st.ReviewsFor(domain.TaskID(*taskID))
	st.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "reviews:", err)
		return exitError
	}
	findings := collectFindings(reviews)
	if len(findings) == 0 {
		fmt.Println("这个任务没有审查意见。")
		return exitOK
	}
	hash := report.FindingsHash(findings)
	if text, cached := report.ReadExplanation(*dir, task.ID, hash); cached {
		fmt.Println(text)
		return exitOK
	}

	lock, lerr := store.AcquireLock(filepath.Join(*dir, "kernel.lock"))
	if lerr != nil {
		fmt.Fprintln(os.Stderr, "a run owns this state dir; wait for it to finish")
		return exitError
	}
	lock.Release()

	backend, closeBackend, err := openSessionBackend(*dir, *executor, resolveRoleModel(*dir, "reporter", *model))
	if err != nil {
		fmt.Fprintln(os.Stderr, "backend:", err)
		return exitError
	}
	defer closeBackend()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	text, err := exec.Summarize(ctx, backend, report.ExplainPrompt(task.Objective, findings))
	if err != nil {
		fmt.Fprintln(os.Stderr, "explain:", err)
		return exitError
	}
	if err := report.WriteExplanation(*dir, task.ID, hash, text); err != nil {
		fmt.Fprintln(os.Stderr, "write explanation:", err)
	}
	fmt.Println(text)
	return exitOK
}

// collectFindings flattens reviews, newest first, and drops duplicates so a
// retry does not repeat the same sentence.
func collectFindings(reviews []domain.Review) []domain.Finding {
	seen := map[string]bool{}
	var out []domain.Finding
	for i := len(reviews) - 1; i >= 0; i-- {
		for _, f := range reviews[i].Findings {
			key := f.Code + "\x00" + f.Message
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, f)
			if len(out) >= 20 {
				return out
			}
		}
	}
	return out
}

// goalContinue extends a finished objective: accepted tasks stay exactly as they
// are, and a small set of new tasks builds on them. It creates a new confirmed
// plan but never starts it, so the human still decides when to run.
func goalContinue(args []string) int {
	fs := flag.NewFlagSet("goal continue", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	instruction := fs.String("goal", "", "what to improve or add")
	which := fs.String("planner", "model", "fake | model")
	model := fs.String("model", "", "provider/model for the planner (default: settings or built-in)")
	executor := fs.String("executor", "opencode", "opencode | ao")
	planFile := fs.String("plan-file", "", "plan JSON for the fake planner")
	maxTasks := fs.Int("max-tasks", 8, "task limit for the new plan")
	projectContext := fs.String("context", "", "bounded project context for the planner")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if strings.TrimSpace(*instruction) == "" {
		fmt.Fprintln(os.Stderr, "continue requires --goal")
		return exitError
	}
	st, err := openGoalStore(*dir)
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			fmt.Fprintf(os.Stderr, "another scheduler owns %s; wait for it to finish\n", *dir)
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
	if obj.Status != goal.StatusCompleted && obj.Status != goal.StatusFailed {
		fmt.Fprintf(os.Stderr, "objective is %s; only a finished objective can be continued\n", obj.Status)
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
	for _, t := range tasks {
		existing[t.ID] = true
		if t.Status == domain.TaskAccepted {
			acceptedIDs = append(acceptedIDs, string(t.ID))
		}
	}

	feedback := "This objective was already delivered. The user wants to extend it:\n" + *instruction +
		"\nKeep every accepted task exactly as it is; add at most 5 new tasks that build on them." +
		"\nAccepted tasks: " + strings.Join(acceptedIDs, ", ")

	var p planner.Planner
	if *which == "fake" {
		raw, err := os.ReadFile(*planFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "plan file:", err)
			return exitError
		}
		var plan goal.Plan
		if err := json.Unmarshal(raw, &plan); err != nil {
			fmt.Fprintln(os.Stderr, "plan file:", err)
			return exitError
		}
		p = &planner.Fake{Plans: []goal.Plan{plan}}
	} else {
		backend, closeBackend, err := openSessionBackend(*dir, *executor, resolveRoleModel(*dir, "planner", *model))
		if err != nil {
			fmt.Fprintln(os.Stderr, "backend:", err)
			return exitError
		}
		defer closeBackend()
		p = planner.NewModel(backend)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	plan, _, err := planner.Generate(ctx, p, planner.Request{
		Goal: obj.Goal, Constraints: obj.Constraints, Feedback: feedback, MaxTasks: *maxTasks,
		Context: *projectContext,
	}, 2)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plan:", err)
		return exitError
	}
	plan = replan.NormalizeAccepted(record.Spec, plan, acceptedIDs)
	if err := replan.ValidateReplacement(record.Spec, plan, acceptedIDs); err != nil {
		fmt.Fprintln(os.Stderr, "continuation rejected:", err)
		return exitError
	}
	if err := rejectHistoricalID(plan, record.Spec, existing); err != nil {
		fmt.Fprintln(os.Stderr, "continuation rejected:", err)
		return exitError
	}
	hash, err := planner.Hash(plan)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hash:", err)
		return exitError
	}
	newIDs := map[string]bool{}
	for _, t := range plan.Tasks {
		newIDs[t.ID] = true
	}
	var retire []domain.TaskID
	for _, t := range tasks {
		if (t.Status == domain.TaskPending || t.Status == domain.TaskBlocked) && !newIDs[string(t.ID)] {
			retire = append(retire, t.ID)
		}
	}
	version := obj.PlanVersion + 1
	if _, err := st.ApplyReplan(obj.ID, version, plan, hash, retire,
		"superseded by plan v"+fmt.Sprint(version)); err != nil {
		fmt.Fprintln(os.Stderr, "continue:", err)
		return exitError
	}
	fmt.Printf("plan v%d ready (%d tasks kept, %d new); objective is active\n",
		version, len(acceptedIDs), len(plan.Tasks)-len(acceptedIDs))
	for _, t := range plan.Tasks {
		fmt.Printf("  %-8s deps=%-10s artifact=%s\n", t.ID, strings.Join(t.Deps, ","), strings.Join(t.Artifacts, ","))
	}
	fmt.Println("next: kernel goal run --dir " + *dir)
	return exitOK
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// backendSummarizer adapts any exec.Backend to report.Summarizer.
type backendSummarizer struct{ backend exec.Backend }

func (s backendSummarizer) Summarize(ctx context.Context, prompt string) (string, error) {
	return exec.Summarize(ctx, s.backend, prompt)
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// goalModels reads or writes the per-role model settings and can refresh the
// cached list of models the harness knows about.
func goalModels(args []string) int {
	fs := flag.NewFlagSet("goal models", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	var sets stringList
	fs.Var(&sets, "set", "role=model (repeatable; empty model clears)")
	refresh := fs.Bool("refresh", false, "refresh the model list from the harness")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if strings.TrimSpace(*dir) == "" {
		fmt.Fprintln(os.Stderr, "models requires --dir")
		return exitError
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "models:", err)
		return exitError
	}
	if *refresh {
		models, err := modelcfg.Refresh(context.Background(), *dir, "")
		if err != nil {
			fmt.Fprintln(os.Stderr, "refresh:", err)
			return exitError
		}
		fmt.Printf("cached %d models\n", len(models))
	}
	settings := modelcfg.Load(*dir)
	for _, pair := range sets {
		role, model, ok := strings.Cut(pair, "=")
		if !ok {
			fmt.Fprintf(os.Stderr, "expected role=model, got %q\n", pair)
			return exitError
		}
		next, err := settings.Set(strings.TrimSpace(role), model)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitError
		}
		settings = next
	}
	if len(sets) > 0 {
		if err := modelcfg.Save(*dir, settings); err != nil {
			fmt.Fprintln(os.Stderr, "models:", err)
			return exitError
		}
	}
	cache := modelcfg.LoadCache(*dir)
	if *asJSON {
		raw, err := json.MarshalIndent(map[string]any{
			"settings": settings, "models": cache.Models, "at": cache.At,
		}, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "models:", err)
			return exitError
		}
		fmt.Println(string(raw))
		return exitOK
	}
	show := func(role, value string) {
		if value == "" {
			value = "(default)"
		}
		fmt.Printf("%-11s %s\n", role, value)
	}
	show("planner", settings.Planner)
	show("worker", settings.Worker)
	show("reviewer", settings.Reviewer)
	show("acceptance", settings.Acceptance)
	show("reporter", settings.Reporter)
	if len(cache.Models) > 0 {
		fmt.Printf("known models: %d (refreshed %s)\n", len(cache.Models), cache.At.Format(time.RFC3339))
	} else {
		fmt.Println("known models: none cached; run with --refresh")
	}
	return exitOK
}

func goalRecover(args []string) int {
	fs := flag.NewFlagSet("goal recover", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	approve := fs.Bool("approve", false, "approve exactly the pending recovery action")
	taskOverride := fs.String("task", "", "explicit task id to requeue after human inspection")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	open := openGoalStoreRead
	if *approve {
		open = openGoalStore
	}
	st, err := open(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return exitError
	}
	defer st.Close()
	obj, ok, err := singleObjective(st)
	if err != nil || !ok {
		fmt.Fprintln(os.Stderr, "no objective in this state dir")
		return exitError
	}
	plan, hasPlan, err := st.GetPlan(obj.ID, obj.PlanVersion)
	if err != nil || !hasPlan {
		fmt.Fprintln(os.Stderr, "confirmed plan not found")
		return exitError
	}
	needsApproval := len(approvalReasons(plan.Spec)) > 0
	pending, hasPending, err := recovery.GetPending(st)
	if err != nil {
		fmt.Fprintln(os.Stderr, "recovery:", err)
		return exitError
	}
	decision, err := recovery.Decide(st, obj, needsApproval,
		recovery.Policy{MaxRecoveries: recovery.Budget(st), AttemptCap: 3, ReplansLeft: 1})
	if err != nil {
		fmt.Fprintln(os.Stderr, "recovery:", err)
		return exitError
	}
	if !*approve {
		report, rerr := progress.Assess(st, obj, needsApproval)
		if rerr == nil {
			fmt.Println(report)
		}
		if hasPending {
			fmt.Printf("pending approval: task=%s category=%s reason=%s\n",
				pending.TaskID, pending.Category, pending.Reason)
		} else {
			fmt.Printf("decision: action=%s task=%s category=%s reason=%s\n",
				decision.Action, decision.TaskID, decision.Category, decision.Reason)
		}
		if decision.Action == "retry" || decision.Action == "approve" {
			fmt.Printf("approve with: kernel goal recover --dir %s --approve\n", *dir)
		}
		return exitOK
	}
	taskID := decision.TaskID
	reason := decision.Reason
	if hasPending {
		taskID, reason = pending.TaskID, pending.Reason
	}
	if strings.TrimSpace(*taskOverride) != "" {
		taskID, reason = strings.TrimSpace(*taskOverride), "explicit human approval after inspection"
	}
	if taskID == "" {
		fmt.Fprintln(os.Stderr, "no pending or recoverable failure")
		return exitBlocked
	}
	if decision.Action == "stop" && !hasPending && strings.TrimSpace(*taskOverride) == "" {
		fmt.Fprintln(os.Stderr, "recovery is not allowed:", reason)
		return exitBlocked
	}
	if hasPending && pending.Category == "unknown_execution_state" && strings.TrimSpace(*taskOverride) == "" {
		fmt.Fprintln(os.Stderr, "unknown outcome requires explicit --task after checking side effects")
		return exitBlocked
	}
	if decision.Action == "replan" && strings.TrimSpace(*taskOverride) == "" {
		fmt.Fprintln(os.Stderr, "this failure needs a bounded replan, not a direct requeue")
		return exitBlocked
	}
	count, cerr := recovery.Increment(st)
	if cerr != nil {
		fmt.Fprintln(os.Stderr, "recovery budget:", cerr)
		return exitError
	}
	ok, err = st.RequeueBlocked(domain.TaskID(taskID), "recovery approved: "+reason, 3)
	if err != nil || !ok {
		fmt.Fprintf(os.Stderr, "requeue %s: ok=%v err=%v\n", taskID, ok, err)
		return exitBlocked
	}
	if err := recovery.ClearPending(st); err != nil {
		fmt.Fprintln(os.Stderr, "clear recovery pending:", err)
	}
	if obj.Status == goal.StatusFailed {
		// A human explicitly approved resuming after inspecting the failure.
		if err := st.MoveObjective(obj.ID, goal.StatusDraft); err != nil {
			fmt.Fprintln(os.Stderr, "reactivate objective:", err)
			return exitError
		}
		if err := st.MoveObjective(obj.ID, goal.StatusPlanned); err != nil {
			fmt.Fprintln(os.Stderr, "reactivate objective:", err)
			return exitError
		}
		if err := st.MoveObjective(obj.ID, goal.StatusActive); err != nil {
			fmt.Fprintln(os.Stderr, "reactivate objective:", err)
			return exitError
		}
	}
	_ = st.AppendEvent(domain.TaskID(taskID), "recovery_approved", reason)
	fmt.Printf("recovery approved for %s (count %d); run `kernel goal run --dir %s`\n", taskID, count, *dir)
	return exitOK
}
