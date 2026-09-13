package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/evals"
)

// TestV1HashesAreFrozen fails if a v1 task's fingerprint changes. v1 is
// immutable: a change in pass rate must stay comparable to the dataset that
// produced it. Edit v2 instead.
func TestV1HashesAreFrozen(t *testing.T) {
	path := filepath.Join("testdata", "hashes.json")
	d := Dataset()
	got := map[string]string{}
	for _, task := range d.Tasks {
		got[task.ID] = evals.TaskHash(task)
	}
	if os.Getenv("GOLUM_UPDATE_GOLDEN") == "1" {
		out, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var golden map[string]string
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(golden) {
		t.Fatalf("task count %d, golden %d — v1 must stay immutable", len(got), len(golden))
	}
	for id, want := range golden {
		if got[id] != want {
			t.Errorf("task %s hash changed:\n  got  %s\n  want %s", id, got[id], want)
		}
	}
	for id := range got {
		if _, ok := golden[id]; !ok {
			t.Errorf("new v1 task %s — add it to v2, not v1", id)
		}
	}
}
