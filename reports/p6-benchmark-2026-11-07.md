# P6.3 Benchmark — 2026-11-07

## Scope

Only **Task A (software development)** was run with a real model. Tasks B/C were
not run because each ProjectOS sample costs several model sessions and several
minutes; this is a small, explicitly limited sample, not a statistical claim.

## Conditions

- Model: `opencode-go/deepseek-v4.1-flash`
- Same prompt for both groups.
- Hidden test (never given to either system): import `calc` and assert
  `add(2,3)==5`, `add(-2,2)==0`, `add(0,0)==0`.
- Isolated `/private/tmp` directories; no real research data.
- Baseline: `opencode run --standalone --format json --auto`.
- ProjectOS: real Planner + two Worker tasks + Reviewer + Acceptance +
  Objective acceptance; `--approve-risk` supplied up front.

Prompt:

> Create calc.py with add(a,b) and test_calc.py with unittest tests for positive,
> negative, and zero cases. Run python3 -m unittest. Do not modify anything
> outside this directory. Finish when the tests pass.

## Results

| Metric | OpenCode baseline | ProjectOS (final) |
|---|---:|---:|
| Hidden test | PASS | PASS |
| Wall clock | 140.1 s | 629.4 s |
| Prompt tokens (provider input) | 6,583 | 45,957 |
| Output tokens | 385 | 1,024 |
| Cache-read tokens (not counted above) | 11,520 | not separately exposed |
| Tasks | 1 session | 2 tasks accepted |
| Reviews / acceptance calls | 0 | 5 |
| Manual interventions | 0 | 0 |
| Usage estimate | provider | partially estimated |
| Delivery | files in cwd | `out/` + delivery.json/md |

ProjectOS token split (final run): planner 6,444/384, worker 11,495/543,
reviewer 8,975/16, acceptance 12,244/16, objective 6,799/65. One reviewer leg
was marked estimated, so total usage is reported as estimated.

## Failures found before the final run

1. **Missing dependency files in worker workdir**  
   T2 imported `calc` but only received its content in the prompt; the actual
   `calc.py` was not in its isolated workspace. Root cause: `StartRequest` had
   no dependency-file seeding. Fixed by materializing accepted ancestor
   artifacts into the worker directory and marking them as seed files.

2. **Objective judge saw an empty workspace**  
   Both tasks were accepted, but the objective judge inspected its empty cwd and
   reported missing files. Fixed by seeding `TaskResults` into the objective
   session (`ReviewRequest.SeedFiles`).

Both are correctness fixes, not benchmark tuning.

## Interpretation

The final ProjectOS run passes the hidden test, but at **4.5× wall time** and
**~7× prompt tokens** versus a single OpenCode session on this small task. The
overhead is explained by the real Planner, two Worker sessions, two Reviewer
passes, two Acceptance passes, and Objective acceptance. This sample does not
show a ProjectOS benefit; it shows the cost of the additional gates.

No default review/acceptance policy was changed from this one sample. A
reasonable next experiment is a low-risk, single-artifact task with review
disabled to measure the isolated DAG/planning overhead, and a larger task where
review catches a real defect. Tasks B/C remain unmeasured.
