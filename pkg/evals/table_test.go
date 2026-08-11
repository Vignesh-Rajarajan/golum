package evals

import (
	"strings"
	"testing"
)

func TestTable(t *testing.T) {
	baseline := New(Options{Name: "baseline"})
	candidate := New(Options{Name: "candidate"})
	rows := Table("demo", baseline, candidate, 2)
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4", len(rows))
	}
	if rows[0].Name != "baseline" || rows[0].Repetition != 1 {
		t.Fatalf("row0 = %+v", rows[0])
	}
	if rows[1].Name != "candidate" || rows[1].Repetition != 1 {
		t.Fatalf("row1 = %+v", rows[1])
	}
	if rows[2].Repetition != 2 || rows[3].Repetition != 2 {
		t.Fatalf("repetitions: %+v %+v", rows[2], rows[3])
	}
}

func TestTable_defaultRepetitions(t *testing.T) {
	rows := Table("x", New(Options{Name: "a"}), New(Options{Name: "b"}), 0)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
}

func TestComputeLift(t *testing.T) {
	base := []Score{{Value: 0}, {Value: 1}, {Value: 0}}
	cand := []Score{{Value: 1}, {Value: 1}, {Value: 1}}
	r := ComputeLift("skill-effectiveness", base, cand)
	if r.N != 3 {
		t.Fatalf("N=%d", r.N)
	}
	if r.BaselinePassRate != 1.0/3.0 {
		t.Fatalf("baseline pass rate %v", r.BaselinePassRate)
	}
	if r.CandidatePassRate != 1.0 {
		t.Fatalf("candidate pass rate %v", r.CandidatePassRate)
	}
	wantLift := (1.0 - 1.0/3.0) * 100
	if r.LiftPercentagePoints != wantLift {
		t.Fatalf("lift=%v want %v", r.LiftPercentagePoints, wantLift)
	}
	if r.BaselineAvgScore != 1.0/3.0 {
		t.Fatalf("baseline avg %v", r.BaselineAvgScore)
	}
	if r.CandidateAvgScore != 1.0 {
		t.Fatalf("candidate avg %v", r.CandidateAvgScore)
	}
	s := r.String()
	if s == "" {
		t.Fatal("empty String()")
	}
}

func TestComputeLift_uneven(t *testing.T) {
	r := ComputeLift("x", []Score{{Value: 1}, {Value: 1}}, []Score{{Value: 0}})
	if r.N != 1 {
		t.Fatalf("N=%d want 1", r.N)
	}
}

func TestResolveModel(t *testing.T) {
	got, err := ResolveModel("explicit", func(string) string { return "env" })
	if err != nil || got != "explicit" {
		t.Fatalf("explicit: %q %v", got, err)
	}
	got, err = ResolveModel("", func(k string) string {
		if k == "GOLUM_EVAL_MODEL" {
			return "eval-model"
		}
		return "openai-model"
	})
	if err != nil || got != "eval-model" {
		t.Fatalf("GOLUM_EVAL_MODEL: %q %v", got, err)
	}
	got, err = ResolveModel("", func(k string) string {
		if k == "OPENAI_MODEL" {
			return "openai-model"
		}
		return ""
	})
	if err != nil || got != "openai-model" {
		t.Fatalf("OPENAI_MODEL: %q %v", got, err)
	}
	_, err = ResolveModel("", func(string) string { return "" })
	if err == nil {
		t.Fatal("expected error when no model")
	}
}

func TestEqualsAndContains(t *testing.T) {
	eq := Equals("Paris")
	s, err := eq(t.Context(), &Result{Output: "Paris"}, "")
	if err != nil || s.Value != 1 {
		t.Fatalf("equals match: %+v %v", s, err)
	}
	s, err = eq(t.Context(), &Result{Output: "Lyon"}, "")
	if err != nil || s.Value != 0 {
		t.Fatalf("equals miss: %+v %v", s, err)
	}
	c := Contains("Paris")
	s, err = c(t.Context(), &Result{Output: "The city is Paris."}, "")
	if err != nil || s.Value != 1 {
		t.Fatalf("contains: %+v %v", s, err)
	}
}

func TestToolCalled(t *testing.T) {
	j := ToolCalled("write_file", func(args map[string]any, result string) bool {
		return args["path"] == "hello.txt" && !strings.Contains(result, "error")
	})
	res := &Result{Events: []TranscriptEvent{
		{Kind: "tool_call", Name: "write_file", ToolCallID: "1", Arguments: map[string]any{"path": "hello.txt"}},
		{Kind: "tool_result", Name: "write_file", ToolCallID: "1", Content: "ok"},
	}}
	s, err := j(t.Context(), res, "")
	if err != nil || s.Value != 1 {
		t.Fatalf("tool called: %+v %v", s, err)
	}
	s, err = ToolCalled("shell", nil)(t.Context(), res, "")
	if err != nil || s.Value != 0 {
		t.Fatalf("missing tool: %+v %v", s, err)
	}
}

func TestExtractJSONObject(t *testing.T) {
	obj, ok := extractJSONObject("here is json:\n```json\n{\"score\": 1, \"rationale\": \"ok\"}\n```\n")
	if !ok || obj == "" {
		t.Fatalf("extract failed: %q", obj)
	}
	s, err := parseJudgeJSON(obj)
	if err != nil || s.Value != 1 || s.Rationale != "ok" {
		t.Fatalf("parse: %+v %v", s, err)
	}
}
