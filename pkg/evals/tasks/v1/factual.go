package v1

import "github.com/Vignesh-Rajarajan/golum/pkg/evals"

// factualTasks are no-tool tasks. They exist to separate "the model cannot
// answer" from "the harness broke": when a filesystem task fails and this one
// also fails, the problem is upstream of anything golum does.
func factualTasks() []evals.Task {
	return []evals.Task{{
		ID:          "factual/capital-of-france",
		Split:       evals.SplitDev,
		Difficulty:  evals.DifficultyEasy,
		Description: "Answer a single factual question with no tools available.",
		Objective:   "What's the capital of France? Reply with only the city name.",
		Tags:        []string{"smoke", "no-tools"},
		Environment: evals.Environment{
			Name:        "default",
			ActiveTools: []string{}, // non-nil and empty: no tools at all
		},
		Acceptance: evals.AcceptanceCriteria{
			Outcome: []evals.OutcomeVerifier{
				evals.FinalAnswerEquals("Paris"),
			},
			Process: []evals.ProcessVerifier{
				// With no tools registered, any tool call is the model
				// inventing one, which the sanitizer is supposed to prevent
				// from reaching the transcript.
				evals.MaxToolCalls(0),
			},
		},
	}}
}
