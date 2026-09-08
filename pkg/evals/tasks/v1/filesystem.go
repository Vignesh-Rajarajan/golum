package v1

import "github.com/Vignesh-Rajarajan/golum/pkg/evals"

func filesystemTasks() []evals.Task {
	return []evals.Task{
		{
			ID:          "fs/write-read-roundtrip",
			Split:       evals.SplitDev,
			Difficulty:  evals.DifficultyEasy,
			Description: "Write a file, read it back, and report the sentinel.",
			Objective: "Write a file named note.txt with exactly the content EVAL_OK. " +
				"Then use read_file to confirm it. Reply with only EVAL_OK when done.",
			Tags: []string{"tools", "filesystem"},
			Environment: evals.Environment{
				Name:        "default",
				ActiveTools: []string{"write_file", "read_file", "edit", "list_dir", "glob"},
			},
			Acceptance: evals.AcceptanceCriteria{
				Outcome: []evals.OutcomeVerifier{
					evals.FileEquals("note.txt", "EVAL_OK"),
					evals.FinalAnswerEquals("EVAL_OK"),
				},
				Process: []evals.ProcessVerifier{
					// The task asks for a read-back, so writing the file and
					// asserting success without checking is a process failure
					// even though the outcome is right.
					evals.ToolCallOrder("write_file", "read_file"),
					evals.NoToolErrors(),
				},
			},
		},
		{
			ID:          "fs/edit-seeded-config",
			Split:       evals.SplitDev,
			Difficulty:  evals.DifficultyMedium,
			Description: "Find and change one value in a seeded config without disturbing the rest.",
			Objective: "The file config/app.json sets \"timeout_seconds\" to 30. " +
				"Change it to 60, leaving every other setting exactly as it is. " +
				"Reply with only DONE when finished.",
			Tags: []string{"tools", "filesystem", "editing"},
			Environment: evals.Environment{
				Name:        "default",
				ActiveTools: []string{"read_file", "edit", "write_file", "list_dir", "glob", "grep"},
			},
			InitialState: evals.InitialState{
				Seed:   "testdata/edit-seeded-config",
				SeedFS: Seeds,
			},
			Acceptance: evals.AcceptanceCriteria{
				Outcome: []evals.OutcomeVerifier{
					evals.FileContains("config/app.json", `"timeout_seconds": 60`),
					// The interesting failure is a rewrite that fixes the
					// target and quietly drops everything around it.
					evals.FileContains("config/app.json", `"service_name": "golum-eval"`),
					evals.FileContains("config/app.json", `"max_retries": 3`),
					evals.FileContains("README.md", "untouched"),
				},
				Process: []evals.ProcessVerifier{
					evals.NoToolErrors(),
				},
			},
		},
	}
}
