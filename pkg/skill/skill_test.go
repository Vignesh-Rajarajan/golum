package skill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
)

func TestLoadSkills(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".golum", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: demo\ndescription: A demo skill\n---\n\nDo the thing carefully.\n"
	if err := os.WriteFile(filepath.Join(dir, "demo.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := execenv.NewOsExecutionEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	skills, err := LoadSkills(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].Name != "demo" {
		t.Fatalf("%+v", skills)
	}
	sec := FormatSkillsSection(skills)
	if !strings.Contains(sec, "# Skills") || !strings.Contains(sec, "Do the thing") {
		t.Fatalf("%q", sec)
	}
}
