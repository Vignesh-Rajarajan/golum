package v1

import (
	"strings"

	"github.com/Vignesh-Rajarajan/golum/pkg/evals"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness"
)

// boundedReadLimit is small enough that reading boundedBlob must be truncated
// before the next model request, and large enough for a usable head/tail preview.
const boundedReadLimit = 512

func boundedBlob() string {
	return "HEAD_MARK\n" + strings.Repeat("x", 20_000) + "\nTAIL_MARK\n"
}

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
		{
			ID:          "fs/read-bounded-output",
			Split:       evals.SplitDev,
			Difficulty:  evals.DifficultyEasy,
			Description: "Read a large file; the harness must bound the tool result.",
			Objective: "Read blob.txt with read_file. The file is large. " +
				"Reply with only the first and last markers you can see, one per line.",
			Tags: []string{"tools", "filesystem", "bounds"},
			Environment: evals.Environment{
				Name:        "default",
				ActiveTools: []string{"read_file"},
				Loop:        harness.LoopConfig{MaxToolResultBytes: boundedReadLimit},
			},
			InitialState: evals.InitialState{
				Files: map[string]string{"blob.txt": boundedBlob()},
			},
			Acceptance: evals.AcceptanceCriteria{
				Outcome: []evals.OutcomeVerifier{
					evals.FinalAnswerContains("HEAD_MARK"),
					evals.FinalAnswerContains("TAIL_MARK"),
				},
				Process: []evals.ProcessVerifier{
					evals.ToolUsed("read_file"),
					evals.ToolResultsWithinBytes(boundedReadLimit),
					evals.ToolTruncationsHaveArtifact(),
					evals.NoToolErrors(),
				},
			},
		},
		{
			ID:          "fs/forced-write",
			Split:       evals.SplitDev,
			Difficulty:  evals.DifficultyEasy,
			Description: "A chatty prompt cannot finish without a successful write_file.",
			Objective:   "Greet the user in one short sentence. Do not mention files.",
			Tags:        []string{"tools", "force-tool"},
			Environment: evals.Environment{
				Name:        "default",
				ActiveTools: []string{"write_file"},
				ForceTool:   "write_file",
			},
			Acceptance: evals.AcceptanceCriteria{
				Process: []evals.ProcessVerifier{
					evals.ToolUsed("write_file"),
					evals.NoToolErrors(),
				},
			},
		},
	}
}
