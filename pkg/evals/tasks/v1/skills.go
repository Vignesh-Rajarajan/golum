package v1

import (
	"github.com/Vignesh-Rajarajan/golum/pkg/evals"
	"github.com/Vignesh-Rajarajan/golum/pkg/skill"
)

// RitualTaskID names the task the skills comparison is built on.
const RitualTaskID = "skills/eval-ritual"

const ritualSkillBody = `When the user asks you to perform the "golum eval ritual", you MUST:

1. First call write_file to create ritual.txt with exactly the content RITUAL_ACK.
2. Only after that file exists, reply with exactly: DONE

Do not skip the file write. Do not add extra commentary after DONE.`

// ritualSkill is the procedure the candidate environment gets and the
// baseline does not.
func ritualSkill() skill.Skill {
	return skill.Skill{
		Name:        "eval-ritual",
		Description: "Procedure for the golum eval ritual task",
		Body:        ritualSkillBody,
	}
}

var ritualTools = []string{"write_file", "read_file", "list_dir"}

// RitualBaseline is the environment without the skill.
func RitualBaseline() evals.Environment {
	return evals.Environment{Name: "baseline-no-skill", ActiveTools: ritualTools}
}

// RitualCandidate is the environment with the skill. The skill itself lives in
// the task's initial state, so switching environments does not change the
// task's fingerprint; see WithRitualSkill.
func RitualCandidate() evals.Environment {
	return evals.Environment{Name: "candidate-with-skill", ActiveTools: ritualTools}
}

// WithRitualSkill returns the task with the procedure seeded into the
// workspace. The skill is part of what the agent wakes up to, so a comparison
// between "has the skill" and "does not" is a comparison between two tasks
// with different initial states, and their differing hashes say so.
func WithRitualSkill(task evals.Task) evals.Task {
	task.InitialState.Skills = []skill.Skill{ritualSkill()}
	return task
}

func skillTasks() []evals.Task {
	return []evals.Task{{
		ID:         RitualTaskID,
		Split:      evals.SplitDev,
		Difficulty: evals.DifficultyMedium,
		Description: "Follow an authored procedure: write a sentinel file first, " +
			"then answer with exactly DONE.",
		Objective: "Perform the golum eval ritual. Follow any applicable skills. " +
			"When finished, reply with only the single word DONE.",
		Tags:        []string{"skills", "procedure"},
		Environment: RitualBaseline(),
		Acceptance: evals.AcceptanceCriteria{
			Outcome: []evals.OutcomeVerifier{
				evals.FileEquals("ritual.txt", "RITUAL_ACK"),
				evals.FinalAnswerEquals("DONE"),
			},
			Process: []evals.ProcessVerifier{
				evals.ToolUsed("write_file"),
			},
		},
	}}
}
