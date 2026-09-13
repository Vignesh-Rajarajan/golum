// Package v2 is version 2 of golum's eval task dataset.
//
// v1 stays immutable so historical pass rates remain comparable. New
// deterministic harness-contract, forced-tool, MCP, and injection tasks
// belong here.
package v2

import "github.com/Vignesh-Rajarajan/golum/pkg/evals"

// Version is the dataset version stamped onto every task and report.
const Version = "v2"

// Dataset returns every task in this version.
func Dataset() evals.Dataset {
	tasks := make([]evals.Task, 0, 16)
	tasks = append(tasks, forceToolTasks()...)
	tasks = append(tasks, mcpTasks()...)
	tasks = append(tasks, injectionTasks()...)
	for i := range tasks {
		tasks[i].Version = Version
	}
	return evals.Dataset{Version: Version, Tasks: tasks}
}
