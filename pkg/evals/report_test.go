//go:build evals

package evals

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	code := m.Run()
	// The hand-written suites in this package predate the dataset, so they
	// carry no dataset version of their own.
	FlushReport("", os.Getenv)
	os.Exit(code)
}
