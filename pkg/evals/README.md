# Golum evals

Behavioral, model-backed checks for golum workflows. They adapt a real
`AgentHarness` / `RunAgentLoop` to Go's `testing` package, run it in an isolated
temporary workspace, and attach native session transcripts as artifacts.

Use them to measure end-to-end behavior and compare prompts, tools, skills,
models, or other harness configurations.

## The five verdicts

Every task is scored on five independent deterministic axes plus an
informational quality axis. They are never blended into one number:

| Axis | Question | Authority |
|---|---|---|
| **Outcome** | Did the workspace and final answer become correct? | Deterministic, authoritative |
| **Process** | Did it use the correct tools, arguments, and order? | Deterministic, authoritative |
| **Safety** | Did it obey approvals, sandbox, and secret rules? | Deterministic, hard gate |
| **Reliability** | Did restart, retry, cancel, and replay behave? | Deterministic, authoritative |
| **Performance** | Did it stay within latency, token, cost, and call budgets? | Deterministic, authoritative |
| **Response** | Was the answer any good? | LLM-judged, informational only |

A fluent answer must never compensate for a policy violation.
`TaskRun.Passed()` requires all five deterministic axes. Subjective judges
never override them.

`pkg/evals/tasks/v1` is frozen (`testdata/hashes.json`). New corpus lives in
`pkg/evals/tasks/v2` and can be driven by `Environment.Script` against a local
OpenAI-compatible server so PRs do not need a real model.

CI gates: PR (`go test` + harness race + dataset validation), merge (full
crash/SQLite/MCP suite), nightly (real-model repetitions), release (holdout +
hash verification). `CompareToBaseline` classifies regressions by axis.

## Running evals

From the repository root:

```bash
scripts/run-evals.sh --model "some/model:free"
```

Eval sources are gated with `//go:build evals`, so a normal `go test ./...`
does not compile or run them. The pure unit tests for statistics, verifiers,
trajectories, attribution, pricing, and the dataset always run.

### Environment

| Variable | Purpose |
|---|---|
| `GOLUM_EVAL_MODEL` | Model under test (else `OPENAI_MODEL`) |
| `GOLUM_EVAL_JUDGE_MODEL` | Model that grades subjective criteria (else the run model) |
| `GOLUM_EVAL_ARTIFACT_DIR` | Where artifacts land (else `.eval/<timestamp>_<uuid>`) |
| `GOLUM_EVAL_SPLIT` | `dev` (default) or `holdout` |
| `GOLUM_EVAL_REPETITIONS` | Runs per task, default 3 |
| `GOLUM_EVAL_K` | k for pass@k and pass^k, default = repetitions |
| `GOLUM_EVAL_TASK` | Run one task by ID |
| `GOLUM_EVAL_SEED` | Fixes randomized pairwise presentation order |
| `GOLUM_EVAL_PRICES` | JSON price table; absent means cost is reported unavailable |

Authentication uses `OPENAI_API_KEY` and/or `OPENROUTER_API_KEY` (OpenRouter
wins when set), as in `pkg/config` / `llm.NewClient`.

A price table looks like:

```json
{ "gpt-5.2": { "input_per_mtok": 2.50, "output_per_mtok": 10.00 } }
```

When no price is configured, cost is reported as unavailable rather than as
zero. An unpriced run that looks free is worse than one that admits it does not
know.

## Tasks

A `Task` states everything an evaluation needs: where the agent starts, what it
is asked to do, what it may use, and what counts as success.

```go
evals.Task{
    ID:         "fs/write-read-roundtrip",
    Split:      evals.SplitDev,
    Difficulty: evals.DifficultyEasy,
    Objective:  "Write note.txt containing EVAL_OK, read it back, reply EVAL_OK.",
    Environment: evals.Environment{
        ActiveTools: []string{"write_file", "read_file"},
    },
    InitialState: evals.InitialState{
        Files:  map[string]string{"config/app.json": "{}"},
        Seed:   "testdata/edit-seeded-config", // copied from an embed.FS
        SeedFS: v1.Seeds,
        Skills: []skill.Skill{{Name: "eval-ritual", Body: "..."}},
    },
    Acceptance: evals.AcceptanceCriteria{
        Outcome: []evals.OutcomeVerifier{evals.FileEquals("note.txt", "EVAL_OK")},
        Process: []evals.ProcessVerifier{evals.ToolCallOrder("write_file", "read_file")},
    },
}
```

Run one with `evals.RunTask(ctx, t, task)`, which seeds the workspace, runs the
steps, and returns a scored `TaskRun`.

### Verifiers

Outcome verifiers read the workspace through the same confined
`execenv.ExecutionEnv` the agent used, so a verifier is subject to the same
boundary as the code it grades:

`FileEquals`, `FileContains`, `FileAbsent`, `CommandSucceeds`,
`FinalAnswerEquals`, `FinalAnswerContains`.

Process verifiers read the trajectory:

`ToolUsed`, `ToolNotUsed`, `ToolCallOrder`, `MaxToolCalls`, `NoToolErrors`,
`NoPolicyViolation`, `ToolResultsWithinBytes`, `ToolTruncationsHaveArtifact`.

`Environment.ForceTool` copies onto the loop so a run cannot complete without a
successful invocation of that tool. Extra MCP operations are reached only
through `invoke`; they never join the native tool roster (`Metrics.NativeToolCount`).
Oversized tool results spill to `.golum/artifacts/` and the preview stays within
`MaxToolResultBytes`.

Both accept custom predicates through `OutcomeFunc` and `ProcessFunc`, and an
existing `Judge` can be lifted onto either axis with `OutcomeFromJudge` /
`ProcessFromJudge` (passing only at a full score of 1).

A verifier's expectation is part of its name, e.g.
`file_equals(note.txt == "EVAL_OK")`, because task fingerprints are built from
verifier names and a check that hid its expectation would let an edited task
masquerade as the same one.

## The dataset

`pkg/evals/tasks/v1` holds version 1 of the task dataset. Tasks are Go values
rather than a data file so verifiers can be ordinary functions; `testdata/`
inside that package holds only inert seed content, embedded so a task's initial
state cannot depend on the working directory.

The version in the package path is load-bearing: editing a task changes what a
pass rate means, so a breaking change belongs in a `v2` package rather than in
edits to `v1`.

### Splits and contamination

Tasks carry a `Split`. `dev` runs by default; `holdout` runs only under
`GOLUM_EVAL_SPLIT=holdout`. Iterating against a holdout task contaminates it:
once it has driven a fix, its pass rate stops measuring generalization and
starts measuring how hard it was fit, and there is no way to un-see it.
Defaulting to dev makes reaching for the holdout set a deliberate act.

Every report carries the dataset version and a per-task hash covering the
objective, initial state, and verifier names, so an edited task shows up as a
dataset change rather than as a phantom regression. Changing a task's
`Environment` deliberately does *not* change its hash: the environment is the
axis a comparison varies.

## Metrics and reporting

Each run records latency, time to first token, model requests, tool calls and
errors, token usage, estimated cost, stop reasons, policy violations,
compactions, and the durable operation outcome. These are read back from the
records the harness already persists rather than measured by a parallel
instrumentation path, so eval numbers and production records cannot drift
apart.

`SummarizeTask(runs, k)` aggregates repetitions into a `TaskReport`:

- separate outcome, process, and response pass rates;
- **pass@k** — the chance at least one of k attempts passes, a capability
  ceiling;
- **pass^k** — the chance all k attempts pass, which is the number that matters
  for anything unattended. An agent with pass@5 of 1.0 and pass^5 of 0.2 fails
  four times out of five when nobody is retrying for it;
- a Wilson confidence interval, used instead of the normal approximation
  because eval samples are small and rates cluster at 0 and 1;
- warnings for small samples, missing token usage, and unavailable cost;
- failure attribution counts.

Both estimators use the unbiased hypergeometric form, so one set of n runs
answers the question for every k ≤ n.

`FlushReport` writes `report.json` and `report.md` into the artifact directory
and prints the markdown to stderr. Both `TestMain`s call it.

## Failure attribution

`AttributeFailure` names the first point where a failed run went wrong:
`policy_violation`, `tool_error`, `wrong_process`, `guardrail`, `no_progress`,
or `premature_completion` — the last being the interesting case where the
trajectory looks clean, the agent declared itself done, and the world does not
match the request.

The rules are mechanical rather than model-judged: attribution is used to
compare runs over time, so it has to give the same answer for the same
trajectory every time.

## Trajectory-prefix replay

`Harness.ReplayFrom(ctx, t, prior, cut, steps...)` re-runs from partway through
a previous trajectory, which is what makes a specific failure testable in
isolation. Re-running a whole task to reach the interesting step wastes the
earlier calls and may not reach the same state twice.

Two things are restored together:

- the conversation, rebuilt from recorded entries. `SafeCutIndex` normalizes
  the cut so an assistant message never loses the results of the tools it
  requested — a provider rejects a context where a tool call has no result;
- the workspace, restored from the snapshot taken after the last tool call in
  the kept prefix. This requires the prior run to have set
  `Environment.SnapshotWorkspace`; without it the files the prefix created
  would be missing.

`ReplayTaskFrom` scores the continuation against the same acceptance criteria,
so "does it recover from here" is measured exactly like "does it succeed from
scratch".

## Comparisons

`TaskTable(evalSet, task, baseline, candidate, repetitions)` expands one task
over two environments. Holding the task fixed and varying only the environment
is what makes the resulting lift attributable to the change under test.
`ComputeLift` reports candidate pass rate minus baseline pass rate in
percentage points.

`NewPairwiseJudge` asks a model which of two runs is better. Presentation order
is randomized per comparison, because judge models systematically favour one
position and a fixed order silently converts that bias into a result. Verdicts
are translated back to the caller's ordering, `TallyPairwise` counts wins by
candidate identity rather than position, and `FirstPositionWinRate` reports how
often the first-shown candidate won so the bias stays visible. Order is
reproducible under `GOLUM_EVAL_SEED`.

## Layout

| Path | What it holds |
|---|---|
| `harness.go` | `Harness.Run`, `Result`, steps, session wiring |
| `task.go` | `Task`, `InitialState`, `Environment`, `AcceptanceCriteria` |
| `verifier.go` | `Check`, outcome, process, safety, reliability, and performance verifiers |
| `safety.go` | Hard-gate safety verifiers |
| `judge_quality.go` | Quality and groundedness judges |
| `regression.go` | Baseline comparison classified by axis |
| `trajectory.go` | `TrajectoryStep`, `SafeCutIndex`, prefix helpers |
| `metrics.go` | Per-run accounting derived from session records |
| `approvals.go` | Approval policies and recorded policy violations |
| `stats.go` | pass@k, pass^k, Wilson intervals, percentiles |
| `attribution.go` | First-bad-step failure attribution |
| `pairwise.go` | Order-randomized pairwise judging and tallies |
| `pricing.go` | Optional price table and cost estimation |
| `dataset.go` | Versioning, splits, task hashing |
| `report.go` | `TaskReport`, `Report`, JSON and markdown writers |
| `replay.go` | Trajectory-prefix replay |
| `artifact.go` | `runs.jsonl` index and session transcripts |
| `tasks/v1/` | Frozen historical task dataset (`testdata/hashes.json`) |
| `tasks/v2/` | Scripted-model contract, force-tool, MCP, and injection tasks |
| `suite/` | The dataset runner (`//go:build evals`) |

The dataset runner lives in its own package because `tasks/v1` and
`tasks/v2` import `pkg/evals`: a test inside package `evals` cannot import
the dataset without an import cycle.

## Artifacts

Each invocation prints an ignored `.eval/` artifact directory. `runs.jsonl`
indexes completed runs, including the five verdict axes, metrics, and
attribution; session transcripts live under `sessions/*.json`; `report.json`
and `report.md` summarize the whole run. Workspace snapshots, when enabled,
live under `snapshots/`.

These files contain prompts, responses, and tool output. `ArtifactDir` returns
an error rather than silently returning `""` if the directory cannot be
created, and callers fail loudly on it — a broken audit trail is a real
failure, not something to shrug past.
