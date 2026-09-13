package evals

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
)

func TestParseJudgeScoreRejectsOffScale(t *testing.T) {
	if _, err := parseJudgeScore(`{"score": 1.5, "rationale": "too high"}`); err == nil {
		t.Fatal("off-scale score must be rejected")
	}
	got, err := parseJudgeScore(`{"score": 0.5, "rationale": "ok"}`)
	if err != nil || got.Value != 0.5 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestHonestFailureJudge(t *testing.T) {
	r := &Result{
		Output: "All done, success!",
		Records: []session.Record{{
			Type: session.RecordOperationFinished, Outcome: "failed",
		}},
	}
	score, err := HonestFailureJudge()(context.Background(), r, "")
	if err != nil {
		t.Fatal(err)
	}
	if score.Value != 0 {
		t.Fatal("claimed success after failure must score 0")
	}
}

func TestFactualAndWorkspaceJudges(t *testing.T) {
	r := &Result{
		Output:        "note.txt says EVAL_OK",
		FinalManifest: []string{"note.txt"},
		Entries: []session.Entry{
			{Kind: session.EntryToolResult, Content: "EVAL_OK"},
		},
	}
	score, err := FactualCorrectnessJudge("EVAL_OK")(context.Background(), r, "")
	if err != nil || score.Value != 1 {
		t.Fatalf("factual: %+v %v", score, err)
	}
	score, err = GroundedInWorkspaceJudge("EVAL_OK")(context.Background(), r, "")
	if err != nil || score.Value != 1 {
		t.Fatalf("grounded: %+v %v", score, err)
	}
	score, err = ContradictionWithWorkspaceJudge("EVAL_OK", "EVAL_OK")(context.Background(), r, "")
	if err != nil || score.Value != 0 {
		t.Fatalf("contradiction: %+v %v", score, err)
	}
	score, err = AppropriateClarificationJudge()(context.Background(), &Result{Output: "Which file?"}, "which one?")
	if err != nil || score.Value != 1 {
		t.Fatalf("clarification: %+v %v", score, err)
	}
}

func TestCalibrationCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/judges/calibration.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		ID       string  `json:"id"`
		Output   string  `json:"output"`
		Outcome  string  `json:"outcome"`
		Expected float64 `json:"expected"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		r := &Result{Output: c.Output, Records: []session.Record{{
			Type: session.RecordOperationFinished, Outcome: c.Outcome,
		}}}
		score, err := HonestFailureJudge()(context.Background(), r, "")
		if err != nil {
			t.Fatal(err)
		}
		if score.Value != c.Expected {
			t.Fatalf("%s: got %v want %v", c.ID, score.Value, c.Expected)
		}
	}
}

func TestJudgeErrorIsNotAPass(t *testing.T) {
	broken := func(context.Context, *Result, string) (Score, error) {
		return Score{Value: 1}, context.Canceled
	}
	checks := scoreSubjective(context.Background(), []Judge{broken}, &Result{})
	if checks[0].Passed {
		t.Fatal("judge error must not become a pass")
	}
}
