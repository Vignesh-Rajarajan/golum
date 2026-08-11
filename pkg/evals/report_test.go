//go:build evals

package evals

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	code := m.Run()
	reports := SnapshotLifts()
	if len(reports) > 0 {
		fmt.Fprintln(os.Stderr, "\n=== eval lift reports ===")
		for _, r := range reports {
			fmt.Fprint(os.Stderr, r.String())
		}
	}
	os.Exit(code)
}
