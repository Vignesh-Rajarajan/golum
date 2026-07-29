package skill

import (
	"context"
	"fmt"
	"strings"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
)

// Skill is a markdown skill loaded from .golum/skills/*.md.
type Skill struct {
	Name        string
	Description string
	Body        string
	RawFront    map[string]string
}

// LoadSkills discovers skills under .golum/skills via the execution env.
func LoadSkills(ctx context.Context, env execenv.ExecutionEnv) ([]Skill, error) {
	entries, err := env.ListDir(ctx, ".golum/skills")
	if err != nil {
		// No skills dir is fine
		return nil, nil
	}
	var out []Skill
	for _, e := range entries {
		if e.IsDir || !strings.HasSuffix(e.Name, ".md") {
			continue
		}
		path := ".golum/skills/" + e.Name
		content, err := env.ReadTextFile(ctx, path)
		if err != nil {
			continue
		}
		sk, err := parseSkill(e.Name, content)
		if err != nil {
			continue
		}
		out = append(out, sk)
	}
	return out, nil
}

func parseSkill(filename, content string) (Skill, error) {
	name := strings.TrimSuffix(filename, ".md")
	front := map[string]string{}
	body := content
	if strings.HasPrefix(content, "---\n") {
		rest := content[4:]
		end := strings.Index(rest, "\n---\n")
		if end >= 0 {
			yamlBlock := rest[:end]
			body = rest[end+5:]
			for _, line := range strings.Split(yamlBlock, "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				k, v, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				front[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
			}
		}
	}
	if n, ok := front["name"]; ok && n != "" {
		name = n
	}
	desc := front["description"]
	return Skill{Name: name, Description: desc, Body: strings.TrimSpace(body), RawFront: front}, nil
}

// FormatSkillsSection builds a system-prompt section from skills.
func FormatSkillsSection(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Skills\n\n")
	b.WriteString("The following project skills are available. Follow them when relevant.\n\n")
	for _, sk := range skills {
		fmt.Fprintf(&b, "## %s\n", sk.Name)
		if sk.Description != "" {
			fmt.Fprintf(&b, "%s\n\n", sk.Description)
		}
		b.WriteString(sk.Body)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}
