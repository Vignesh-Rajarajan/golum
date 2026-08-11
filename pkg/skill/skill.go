package skill

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
)

// Skill is a markdown skill loaded from .golum/skills/*.md.
type Skill struct {
	Name                   string
	Description            string
	Body                   string
	RawFront               map[string]string
	FilePath               string
	DisableModelInvocation bool
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
		sk.FilePath = path
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
	disable := strings.EqualFold(front["disable-model-invocation"], "true") ||
		strings.EqualFold(front["disable_model_invocation"], "true")
	return Skill{Name: name, Description: desc, Body: strings.TrimSpace(body), RawFront: front,
		DisableModelInvocation: disable}, nil
}

// FormatSkillsSection builds a system-prompt section from skills.
func FormatSkillsSection(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Skills\n\n<available_skills>\n")
	for _, sk := range skills {
		if sk.DisableModelInvocation {
			continue
		}
		fmt.Fprintf(&b, "<skill><name>%s</name><description>%s</description><location>%s</location>",
			xmlEscape(sk.Name), xmlEscape(sk.Description), xmlEscape(sk.FilePath))
		if sk.Body != "" {
			fmt.Fprintf(&b, "<instructions>%s</instructions>", xmlEscape(sk.Body))
		}
		b.WriteString("</skill>\n")
	}
	b.WriteString("</available_skills>")
	return b.String()
}

func FormatSkillInvocation(sk Skill, extra string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<skill_invocation><name>%s</name><location>%s</location>",
		xmlEscape(sk.Name), xmlEscape(sk.FilePath))
	if extra != "" {
		fmt.Fprintf(&b, "<extra>%s</extra>", xmlEscape(extra))
	}
	fmt.Fprintf(&b, "<instructions>%s</instructions></skill_invocation>", xmlEscape(sk.Body))
	return b.String()
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
