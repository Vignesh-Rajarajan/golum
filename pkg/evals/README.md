# Golum evals

Behavioral, model-backed checks for golum workflows. They adapt a real
`AgentHarness` / `RunAgentLoop` to Go's `testing` package, run it in an isolated
temporary workspace, and attach native session transcripts as artifacts.

Use them to measure end-to-end behavior and compare prompts, tools, skills,
models, or other harness configurations — a different axis from the transport
integration plan in [`docs/INTEGRATION_TESTS.md`](../../docs/INTEGRATION_TESTS.md).

## Running evals

From the repository root:

```bash
scripts/run-evals.sh --model "nvidia/nemotron-3.5-lightning:free"
```

Confirmed working against that model (an OpenRouter free-tier model — set
`OPENROUTER_API_KEY`): `TestSmoke`, `TestToolsFileRoundTrip`, and
`TestReloadAuthoredSkill` pass cleanly. `TestSkillsComparative` fires 6
sequential model calls plus 6 judge calls and is paced 12s apart per row to
respect OpenRouter's free-tier rate limit, but free `:free` models are also
subject to an **account-wide daily quota** (`free-models-per-day`, shared
across every `:free` model, not just the one selected) — if you've been
exercising other free models on the same account that day, comparative runs
may return `429 Too Many Requests` regardless of pacing. That's an OpenRouter
account limit, not a harness bug; either wait for the daily reset, add
OpenRouter credits, or point `--model`/`GOLUM_EVAL_MODEL` at a paid model for
that suite.

Model selection precedence (same idea as pi's `PI_PROVIDER`/`PI_MODEL`):

1. Harness `Options.Model`
2. `GOLUM_EVAL_MODEL`
3. `OPENAI_MODEL`

Judge model precedence is independent, so a comparative eval isn't graded by
the same model that produced the output by default:

1. Explicit judge model passed at the call site
2. `GOLUM_EVAL_JUDGE_MODEL`
3. Falls back to the resolved run model (only when no judge model is configured)

Authentication uses the process environment: `OPENAI_API_KEY` and/or
`OPENROUTER_API_KEY` (OpenRouter wins when set), as in `pkg/config` / `llm.NewClient`.
Some providers/models (confirmed for `nvidia/nemotron-3.5-lightning:free`) don't
report token usage over streaming even with it requested — `smoke_test.go`
treats that as a soft signal (logged, not asserted) rather than assuming every
provider reports it.

Additional arguments are forwarded to `go test`:

```bash
scripts/run-evals.sh --model "nvidia/nemotron-3.5-lightning:free" -run TestSmoke
scripts/run-evals.sh --model "nvidia/nemotron-3.5-lightning:free" -run TestSkillsComparative -v
```

Eval sources are gated with `//go:build evals`, so normal `go test ./...` does
not compile or run them. Pure unit tests for lift/table helpers always run.

Each invocation prints an ignored `pkg/evals/.eval/` artifact directory.
`runs.jsonl` indexes completed harness runs; session transcripts live under
`sessions/*.json`. These files may contain prompts, responses, and tool output.
`ArtifactDir()` returns an error (rather than silently returning "") if the
directory can't be created, and `writeArtifact` fails the test loudly on that —
a broken audit trail is treated as a real failure, not a thing to shrug past.

## Writing evals

```go
//go:build evals

package evals

func TestSmoke(t *testing.T) {
    h := New(Options{Name: "smoke", ActiveTools: []string{}}) // no tools
    result, err := h.Run(ctx, t, Prompt("What's the capital of France? Reply with only the city name."))
    // assert result.Output, result.Usage, result.Events…
}
```

### Configuring the harness

`New(Options{...})` accepts:

| Field | Purpose |
|---|---|
| `Name` | Stable harness identity for reports and comparisons |
| `Model` | Optional override of `GOLUM_EVAL_MODEL` / `OPENAI_MODEL` |
| `ActiveTools` | `nil` = all default tools; empty slice = no tools; otherwise a subset |
| `Skills` | Pre-seeded skills written under `<workspace>/.golum/skills/` before construction |
| `TransformSystemPrompt` | Transforms the assembled default system prompt (via a `TransformContext` hook) |
| `Loop` | Overrides eval loop defaults (`MaxWallClock: 2m`, `MaxModelInvocations: 10`) |

A run accepts one or more steps — `Prompt(text)` or `Reload` (re-load skills from
disk, rebuild the system prompt, replay the in-memory session into a new
`AgentHarness`):

```go
result, err := h.Run(ctx, t,
    Prompt("Create a skill file under .golum/skills/."),
    Reload,
    Prompt("Use the skill you just authored."),
)
```

### Judges

Deterministic judges:

- `Equals(expected)`
- `Contains(substr)`
- `ToolCalled(name, match)`

Model-backed, with its judge model resolved independently of the run model
(`ResolveJudgeModel`) so a comparative eval isn't graded by the same model that
produced the output:

```go
judgeModel, _ := ResolveJudgeModel("", runModel, os.Getenv) // GOLUM_EVAL_JUDGE_MODEL, else runModel
judge := NewLLMJudge(llm.NewClient(cfg), judgeModel, "Pass if the agent followed procedure X…")
score, err := judge(ctx, result, input)
```
Scores from `NewLLMJudge` are graded confidence in `[0,1]`, not forced to a hard
0/1 — a rubric can ask for "1 = fully satisfied, 0.5 = partially, 0 = not
attempted" and the value survives into `LiftReport.AvgScore`. `PassRate` still
treats only `Value >= 1` as a pass, matching pi's own "at least 1 counts as a
pass" convention.

### Comparative eval sets

```go
rows := Table("skill-effectiveness", baseline, candidate, 6)
for i, row := range rows {
    if i > 0 { time.Sleep(pacing) } // free-tier providers rate-limit per minute
    t.Run(…, func(t *testing.T) {
        result, err := row.Harness.Run(ctx, t, Prompt(…))
        if err != nil {
            // A guardrail tripping (invocation limit, tool-call limit, wall
            // clock) or the model not finishing is a score of 0 — that's the
            // signal a comparative eval measures — not a broken test. Only a
            // nil result (harness never started) is a hard failure.
        }
        score, err := judge(ctx, result, input)
        // A judge call failing is also scored 0 with an "unavailable" rationale
        // rather than crashing the run over one flaky request.
        // record score; do not t.Fatal on a low score
    })
}
report := ComputeLift(rows[0].EvalSet, baselineScores, candidateScores)
RecordLift(report) // printed from TestMain after all subtests
```

Comparative suites **record** scores (`t.Logf` + artifacts); they do not fail the
test on a low score. Only structural invariants (no panic, non-empty output,
valid transcript, harness actually starting) are hard assertions. `skills_test.go`
additionally cross-checks the candidate's judge score against ground truth
(does `ritual.txt` actually exist with the right content) and downgrades a
judge-claimed pass to 0 if they disagree — a judge can hallucinate a pass, so
the deterministic check gates it rather than just being logged alongside it.

Lift is candidate pass rate minus baseline pass rate in percentage points, where
a score of `Value >= 1` counts as a pass. `Row.EvalSet` carries the eval-set name
so `ComputeLift`/`RecordLift` don't need it threaded through separately.

## Suites

| File | What it checks |
|---|---|
| `smoke_test.go` | No-tools factual answer (`Paris`) |
| `tools_test.go` | `write_file` → `read_file` round-trip in the temp workspace |
| `skills_test.go` | Baseline vs skill-equipped candidate, LLM-judged, lift reported |
| `reload_test.go` | Model authors a skill, `Reload`, then uses it |
| `table_test.go` | Unit tests for `Table` / `ComputeLift` (no build tag, no API calls) |
