//go:build evals

package evals

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/skill"
)

const skillsEvalSet = "ritual-skill-effectiveness"

// evalRowPacing spaces out sequential real-model calls in a comparative table.
// Free-tier providers commonly enforce a strict per-minute quota; this is
// deliberately conservative since the whole suite runs rarely, not in a tight
// loop.
const evalRowPacing = 12 * time.Second

const ritualPrompt = `Perform the golum eval ritual. Follow any applicable skills. When finished, reply with only the single word DONE.`

const ritualSkillBody = `When the user asks you to perform the "golum eval ritual", you MUST:

1. First call write_file to create ritual.txt with exactly the content RITUAL_ACK.
2. Only after that file exists, reply with exactly: DONE

Do not skip the file write. Do not add extra commentary after DONE.`

func TestSkillsComparative(t *testing.T) {
	requireAPIKey(t)

	baseline := New(Options{
		Name:        "baseline-no-skill",
		ActiveTools: []string{"write_file", "read_file", "list_dir"},
	})
	candidate := New(Options{
		Name:        "candidate-with-skill",
		ActiveTools: []string{"write_file", "read_file", "list_dir"},
		Skills: []skill.Skill{{
			Name:        "eval-ritual",
			Description: "Procedure for the golum eval ritual task",
			Body:        ritualSkillBody,
		}},
	})

	const repetitions = 3
	rows := Table(skillsEvalSet, baseline, candidate, repetitions)

	runModel := mustModel(t)
	judgeModel, err := ResolveJudgeModel("", runModel, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	// Judge with an independently-resolvable model (GOLUM_EVAL_JUDGE_MODEL) so a
	// comparative run isn't graded by the same model that produced the output.
	// Falls back to the run model when no judge model is configured.
	judgeClient := llm.NewClient(buildConfig(judgeModel))
	judge := NewLLMJudge(judgeClient, judgeModel, `Pass (score=1) only if the agent clearly followed the ritual procedure:
it wrote ritual.txt (or equivalent) with RITUAL_ACK via a tool before finishing, and the final answer is DONE.
Score 0.5 if it partially followed the procedure (e.g. wrote the file but the final reply wasn't exactly DONE,
or vice versa). Score 0 if it skipped the file write entirely or invented a different procedure.`)

	type keyed struct {
		name string
		rep  int
	}
	var mu sync.Mutex
	scoresBy := map[keyed]Score{}

	for i, row := range rows {
		row := row
		// Pace real requests: a comparative table fires many sequential model +
		// judge calls, and free-tier providers commonly enforce a strict
		// per-minute quota shared across a model family. Skip the wait before
		// the first row.
		if i > 0 {
			time.Sleep(evalRowPacing)
		}
		t.Run(fmt.Sprintf("%s/rep%d", row.Name, row.Repetition), func(t *testing.T) {
			result, err := row.Harness.Run(context.Background(), t, Prompt(ritualPrompt))
			if err != nil {
				// A comparative baseline is *expected* to fail sometimes — that's
				// the signal this eval measures. A guardrail tripping (model
				// invocation limit, tool-call limit, wall clock) or the model
				// simply not finishing the task within budget is a score of 0,
				// not a broken test. Harness.Run still returns a partial Result
				// on error (Events/Entries/Elapsed populated); only a nil result
				// means the harness never even started, which is structural.
				if result == nil {
					t.Fatalf("run: harness did not start: %v", err)
				}
				t.Logf("harness=%s rep=%d run error (scored 0): %v", row.Name, row.Repetition, err)
				score := Score{Value: 0, Rationale: fmt.Sprintf("run error: %v", err)}
				writeArtifact(t, result, []Score{score})
				mu.Lock()
				scoresBy[keyed{row.Name, row.Repetition}] = score
				mu.Unlock()
				return
			}
			// Structural invariants only — soft scores are recorded, not asserted.
			if strings.TrimSpace(result.Output) == "" {
				t.Fatal("empty output")
			}
			if len(result.Entries) == 0 {
				t.Fatal("empty transcript entries")
			}
			workspaceOutsideHomeGolum(t, result.Workspace)

			score, err := judge(context.Background(), result, ritualPrompt)
			if err != nil {
				// A judge call failing (rate limit, transient network error) is
				// an infrastructure hiccup, not evidence the agent failed the
				// task — but ComputeLift takes plain paired slices rather than
				// optional observations, so treat it as a scored 0 with an
				// explicit "judge unavailable" rationale rather than crashing
				// the whole comparative run over one flaky judge call.
				t.Logf("harness=%s rep=%d judge unavailable (scored 0): %v", row.Name, row.Repetition, err)
				score = Score{Value: 0, Rationale: fmt.Sprintf("judge unavailable: %v", err)}
				writeArtifact(t, result, []Score{score})
				mu.Lock()
				scoresBy[keyed{row.Name, row.Repetition}] = score
				mu.Unlock()
				return
			}

			// Ground-truth gate for the candidate: a judge can hallucinate a
			// pass. If it scored >=1 but ritual.txt doesn't actually exist with
			// the required content, downgrade to 0 rather than just logging it.
			// This never overrides the other direction (file present but score
			// <1) — the rubric also requires the exact final reply, which the
			// file alone can't prove.
			if row.Name == candidate.Name && score.Value >= 1 {
				raw, readErr := os.ReadFile(filepath.Join(result.Workspace, "ritual.txt"))
				if readErr != nil || strings.TrimSpace(string(raw)) != "RITUAL_ACK" {
					t.Logf("judge/ground-truth disagreement: judge scored %.2f but ritual.txt missing or wrong (read err=%v)",
						score.Value, readErr)
					score = Score{
						Value:     0,
						Rationale: fmt.Sprintf("ground-truth override: ritual.txt missing/incorrect despite judge score %.2f (%s)", score.Value, score.Rationale),
					}
				}
			}

			t.Logf("harness=%s rep=%d score=%.2f rationale=%s output=%q",
				row.Name, row.Repetition, score.Value, score.Rationale, result.Output)
			writeArtifact(t, result, []Score{score})

			mu.Lock()
			scoresBy[keyed{row.Name, row.Repetition}] = score
			mu.Unlock()
		})
	}

	baseScores := make([]Score, 0, repetitions)
	candScores := make([]Score, 0, repetitions)
	for i := 1; i <= repetitions; i++ {
		baseScores = append(baseScores, scoresBy[keyed{baseline.Name, i}])
		candScores = append(candScores, scoresBy[keyed{candidate.Name, i}])
	}
	report := ComputeLift(rows[0].EvalSet, baseScores, candScores)
	RecordLift(report)
	t.Logf("\n%s", report.String())
}

func mustModel(t *testing.T) string {
	t.Helper()
	m, err := ResolveModel("", os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
