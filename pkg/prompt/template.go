package prompt

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
)

type Template struct {
	Name, Body, FilePath string
}

func LoadTemplates(ctx context.Context, env execenv.ExecutionEnv) ([]Template, error) {
	entries, err := env.ListDir(ctx, ".golum/commands")
	if err != nil {
		return nil, nil
	}
	var out []Template
	for _, entry := range entries {
		if entry.IsDir || !strings.HasSuffix(entry.Name, ".md") {
			continue
		}
		path := ".golum/commands/" + entry.Name
		body, err := env.ReadTextFile(ctx, path)
		if err != nil {
			continue
		}
		out = append(out, Template{
			Name: strings.TrimSuffix(entry.Name, ".md"), Body: strings.TrimSpace(body), FilePath: path,
		})
	}
	return out, nil
}

func ParseCommandArgs(input string) ([]string, error) {
	var args []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			args = append(args, current.String())
			current.Reset()
		}
	}
	for _, r := range input {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
		} else if r == ' ' || r == '\t' || r == '\n' {
			flush()
		} else {
			current.WriteRune(r)
		}
	}
	if escaped {
		current.WriteRune('\\')
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	flush()
	return args, nil
}

func SubstituteArgs(body string, args []string) string {
	out := strings.ReplaceAll(body, "$ARGUMENTS", strings.Join(args, " "))
	for i := len(args); i >= 1; i-- {
		out = strings.ReplaceAll(out, "$"+strconv.Itoa(i), args[i-1])
	}
	return out
}

func FormatPromptTemplateInvocation(t Template, args []string) string {
	return fmt.Sprintf("<prompt_template name=%q location=%q>\n%s\n</prompt_template>",
		t.Name, t.FilePath, SubstituteArgs(t.Body, args))
}
