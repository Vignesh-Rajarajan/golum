package v2

import (
	"github.com/Vignesh-Rajarajan/golum/pkg/evals"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/harnesstest"
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
			ActiveTools: []string{"invoke"},
			MCPBackends: []mcp.Backend{mcp.Echo()},
			Script: []harnesstest.Turn{
				{ToolCalls: []harnesstest.ToolCall{{
					ID: "c1", Name: "invoke", Args: `{"action":"list"}`,
				}}},
				{ToolCalls: []harnesstest.ToolCall{{
					ID: "c2", Name: "invoke",
					Args: `{"action":"call","server":"echo","name":"echo","arguments":{"text":"ECHO_OK"}}`,
				}}},
				{Content: "ECHO_OK"},
			},
		},
			Acceptance: evals.AcceptanceCriteria{
			Outcome: []evals.OutcomeVerifier{evals.FinalAnswerEquals("ECHO_OK")},
			Process: []evals.ProcessVerifier{
				evals.ToolUsed("invoke"),
				evals.ToolNotUsed("echo"),
				evals.NoToolErrors(),
			},
		},
	}, {
		ID:          "mcp/invoke-unknown-op",
		Split:       evals.SplitDev,
		Difficulty:  evals.DifficultyEasy,
		Description: "Calling an unknown extra operation must fail closed and stay off the native roster.",
		Objective:   "Use invoke to call nope on the echo server, then reply UNKNOWN.",
		Tags:        []string{"mcp", "invoke"},
		Environment: evals.Environment{
			ActiveTools: []string{"invoke"},
			MCPBackends: []mcp.Backend{mcp.Echo()},
			Script: []harnesstest.Turn{
				{ToolCalls: []harnesstest.ToolCall{{
					ID: "c1", Name: "invoke",
					Args: `{"action":"call","server":"echo","name":"nope"}`,
				}}},
				{Content: "UNKNOWN"},
			},
		},
		Acceptance: evals.AcceptanceCriteria{
			Outcome: []evals.OutcomeVerifier{evals.FinalAnswerEquals("UNKNOWN")},
			Process: []evals.ProcessVerifier{evals.ToolUsed("invoke"), evals.ToolNotUsed("nope")},
		},
	}}
}
