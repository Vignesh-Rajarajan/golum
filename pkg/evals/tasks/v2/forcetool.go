package v2

import (
	"github.com/Vignesh-Rajarajan/golum/pkg/evals"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/harnesstest"
)

func forceToolTasks() []evals.Task {
	writeOK := harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{{
		ID: "call_w", Name: "write_file", Args: `{"path":"note.txt","content":"EVAL_OK"}`,
	}}}
	done := harnesstest.Turn{Content: "EVAL_OK"}
	chat := harnesstest.Turn{Content: "hello there"}

	return []evals.Task{
		{
			ID:          "force/write-succeeds",
			Split:       evals.SplitDev,
			Difficulty:  evals.DifficultyEasy,
			Description: "Required tool is called successfully on the first attempt.",
			Objective:   "Greet the user. Do not mention files.",
			Tags:        []string{"force-tool"},
			Environment: evals.Environment{
				ActiveTools: []string{"write_file"},
				ForceTool:   "write_file",
				Script:      []harnesstest.Turn{writeOK, done},
			},
			Acceptance: evals.AcceptanceCriteria{
				Outcome: []evals.OutcomeVerifier{evals.FileEquals("note.txt", "EVAL_OK")},
				Process: []evals.ProcessVerifier{
					evals.ToolUsed("write_file"),
					evals.ToolSucceeded("write_file"),
				},
			},
		},
		{
			ID:          "force/write-after-corrective",
			Split:       evals.SplitDev,
			Difficulty:  evals.DifficultyEasy,
			Description: "Required tool omitted once, then called after corrective inference.",
			Objective:   "Greet the user. Do not mention files.",
			Tags:        []string{"force-tool"},
			Environment: evals.Environment{
				ActiveTools: []string{"write_file"},
				ForceTool:   "write_file",
				Script:      []harnesstest.Turn{chat, writeOK, done},
			},
			Acceptance: evals.AcceptanceCriteria{
				Outcome: []evals.OutcomeVerifier{evals.FileEquals("note.txt", "EVAL_OK")},
				Process: []evals.ProcessVerifier{evals.ToolUsed("write_file")},
			},
		},
		{
			ID:          "force/write-omitted-until-limit",
			Split:       evals.SplitDev,
			Difficulty:  evals.DifficultyEasy,
			Description: "Required tool omitted until the retry limit; the run must fail closed.",
			Objective:   "Greet the user. Do not mention files.",
			Tags:        []string{"force-tool"},
			Environment: evals.Environment{
				ActiveTools: []string{"write_file"},
				ForceTool:   "write_file",
				Script:      []harnesstest.Turn{chat, chat},
			},
			Acceptance: evals.AcceptanceCriteria{
				Process: []evals.ProcessVerifier{evals.ToolNotUsed("write_file")},
				Reliability: []evals.ReliabilityVerifier{
					evals.OperationOutcome("failed"),
				},
			},
		},
		{
			ID:          "force/write-wrong-args",
			Split:       evals.SplitDev,
			Difficulty:  evals.DifficultyEasy,
			Description: "Required tool is called with invalid arguments and must not count as success.",
			Objective:   "Greet the user.",
			Tags:        []string{"force-tool"},
			Environment: evals.Environment{
				ActiveTools: []string{"write_file"},
				ForceTool:   "write_file",
				Script: []harnesstest.Turn{{ToolCalls: []harnesstest.ToolCall{{
					ID: "c1", Name: "write_file", Args: `{"path":""}`,
				}}}},
			},
			Acceptance: evals.AcceptanceCriteria{
				Process: []evals.ProcessVerifier{evals.ToolUsed("write_file")},
				Reliability: []evals.ReliabilityVerifier{
					evals.OperationOutcome("failed"),
				},
			},
		},
		{
			ID:          "force/write-wrong-final-answer",
			Split:       evals.SplitDev,
			Difficulty:  evals.DifficultyEasy,
			Description: "Required tool succeeded; a wrong final answer is still allowed to complete.",
			Objective:   "Write note.txt with EVAL_OK and reply EVAL_OK.",
			Tags:        []string{"force-tool"},
			Environment: evals.Environment{
				ActiveTools: []string{"write_file"},
				ForceTool:   "write_file",
				Script:      []harnesstest.Turn{writeOK, {Content: "WRONG"}},
			},
			Acceptance: evals.AcceptanceCriteria{
				Outcome: []evals.OutcomeVerifier{evals.FileEquals("note.txt", "EVAL_OK")},
				Process: []evals.ProcessVerifier{
					evals.ToolUsed("write_file"),
					evals.ToolSucceeded("write_file"),
				},
			},
		},
	}
}
