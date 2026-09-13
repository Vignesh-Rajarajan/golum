package v1

import (
	"github.com/Vignesh-Rajarajan/golum/pkg/evals"
	"github.com/Vignesh-Rajarajan/golum/pkg/mcp"
)

func mcpTasks() []evals.Task {
	return []evals.Task{{
		ID:          "mcp/invoke-echo",
		Split:       evals.SplitDev,
		Difficulty:  evals.DifficultyEasy,
		Description: "Reach an extra operation only through invoke, never as a native tool.",
		Objective: "Use the invoke tool to list extra operations, then call the echo operation " +
			"on the echo server so it returns ECHO_OK. Reply with only ECHO_OK.",
		Tags: []string{"mcp", "invoke"},
		Environment: evals.Environment{
			Name:        "default",
			ActiveTools: []string{"invoke"},
			MCPBackends: []mcp.Backend{mcp.Echo()},
		},
		Acceptance: evals.AcceptanceCriteria{
			Outcome: []evals.OutcomeVerifier{
				evals.FinalAnswerEquals("ECHO_OK"),
			},
			Process: []evals.ProcessVerifier{
				evals.ToolUsed("invoke"),
				evals.ToolNotUsed("echo"),
				evals.NoToolErrors(),
			},
		},
	}}
}
