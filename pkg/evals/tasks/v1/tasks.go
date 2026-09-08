// Package v1 is version 1 of golum's eval task dataset.
//
// Tasks are Go values rather than a data file so verifiers can be ordinary
// functions: a check like "the tests pass" or "it never called shell" is
// program logic, and expressing it in a schema would mean inventing a language
// to say what Go already says. testdata/v1 holds only inert seed content.
//
// The version in the package path is load-bearing. Editing a task changes what
// a pass rate means, so historical comparisons need a fixed dataset to point
// at; a breaking change to existing tasks belongs in a v2 package rather than
// in edits here.
package v1

import (
	"embed"
	"io/fs"

	"github.com/Vignesh-Rajarajan/golum/pkg/evals"
)

// Version is the dataset version stamped onto every task and report.
const Version = "v1"

//go:embed testdata
var seedRoot embed.FS

// Seeds is the embedded seed tree. Seed content is embedded rather than read
// from disk so a task's initial state cannot depend on the working directory
// the test happened to run in.
var Seeds fs.FS = seedRoot

// Dataset returns every task in this version.
func Dataset() evals.Dataset {
	tasks := make([]evals.Task, 0, 8)
	tasks = append(tasks, factualTasks()...)
	tasks = append(tasks, filesystemTasks()...)
	tasks = append(tasks, skillTasks()...)
	for i := range tasks {
		tasks[i].Version = Version
	}
	return evals.Dataset{Version: Version, Tasks: tasks}
}
