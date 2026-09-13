//go:build evals

package suite_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/evals"
	v1 "github.com/Vignesh-Rajarajan/golum/pkg/evals/tasks/v1"
)

func TestMain(m *testing.M) {
	code := m.Run()
	evals.FlushReport(v1.Version, os.Getenv)
	fmt.Fprintln(os.Stderr)
	os.Exit(code)
}
