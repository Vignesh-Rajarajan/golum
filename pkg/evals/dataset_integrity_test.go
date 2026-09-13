package evals

import (
	"testing"
)

func TestDatasetIntegrityHelpers(t *testing.T) {
	d := Dataset{Version: "vtest", Tasks: []Task{
		{
			ID: "a", Version: "vtest", Split: SplitDev, Difficulty: DifficultyEasy,
			Objective: "do a", Acceptance: AcceptanceCriteria{Outcome: []OutcomeVerifier{FinalAnswerEquals("x")}},
		},
		{
			ID: "b", Version: "vtest", Split: SplitHoldout, Difficulty: DifficultyHard,
			Objective: "do b", Acceptance: AcceptanceCriteria{Process: []ProcessVerifier{ToolUsed("read_file")}},
		},
	}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	dev := d.Filter(SplitDev)
	if len(dev) != 1 || dev[0].ID != "a" {
		t.Fatalf("holdout leaked into default filter: %+v", dev)
	}
	ids := []string{d.Tasks[0].ID, d.Tasks[1].ID}
	if ids[0] != "a" || ids[1] != "b" {
		t.Fatal("dataset ordering drifted")
	}
	base := TaskHash(d.Tasks[0])
	envChanged := d.Tasks[0]
	envChanged.Environment.ActiveTools = []string{"shell"}
	if TaskHash(envChanged) != base {
		t.Fatal("environment change must not alter the fingerprint")
	}
	objChanged := d.Tasks[0]
	objChanged.Objective = "other"
	if TaskHash(objChanged) == base {
		t.Fatal("objective change must alter the fingerprint")
	}
}

func TestDatasetRejectsDuplicateIDsAndEmptyAcceptance(t *testing.T) {
	if err := (Dataset{Version: "v", Tasks: []Task{
		{ID: "a", Version: "v", Objective: "x", Acceptance: AcceptanceCriteria{Outcome: []OutcomeVerifier{FinalAnswerEquals("x")}}},
		{ID: "a", Version: "v", Objective: "y", Acceptance: AcceptanceCriteria{Outcome: []OutcomeVerifier{FinalAnswerEquals("y")}}},
	}}).Validate(); err == nil {
		t.Fatal("duplicate IDs must fail")
	}
	if err := (Task{ID: "z", Objective: "go"}).Validate(); err == nil {
		t.Fatal("empty acceptance must fail")
	}
	if err := (Task{Objective: "go", Acceptance: AcceptanceCriteria{Outcome: []OutcomeVerifier{FinalAnswerEquals("x")}}}).Validate(); err == nil {
		t.Fatal("empty ID must fail")
	}
}

func TestDuplicateVerifierNamesWarnViaHash(t *testing.T) {
	task := Task{
		ID: "dup", Version: "v", Split: SplitDev, Difficulty: DifficultyEasy,
		Objective: "go",
		Acceptance: AcceptanceCriteria{
			Process: []ProcessVerifier{ToolUsed("a"), ToolUsed("a")},
		},
	}
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	// Duplicate names are allowed but hashed twice so they remain visible.
	if TaskHash(task) == "" {
		t.Fatal("expected a hash")
	}
}
