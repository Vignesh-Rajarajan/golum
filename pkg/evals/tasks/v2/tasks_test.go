package v2

import (
	"testing"
)

func TestDatasetIsValid(t *testing.T) {
	d := Dataset()
	if err := d.Validate(); err != nil {
		t.Fatalf("dataset invalid: %v", err)
	}
	if len(d.Tasks) == 0 {
		t.Fatal("v2 dataset is empty")
	}
	for _, task := range d.Tasks {
		if task.Version != Version {
			t.Fatalf("task %q has version %q, want %q", task.ID, task.Version, Version)
		}
		if task.Split == "" || task.Difficulty == "" {
			t.Fatalf("task %q missing split or difficulty", task.ID)
		}
	}
}
