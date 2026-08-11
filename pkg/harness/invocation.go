package harness

import (
	"context"

	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/skill"
)

func (h *AgentHarness) Skill(ctx context.Context, name, extra string) (<-chan AgentEvent, error) {
	for _, candidate := range h.skills {
		if candidate.Name == name {
			return h.Prompt(ctx, skill.FormatSkillInvocation(candidate, extra))
		}
	}
	return nil, &UnknownSkillError{Name: name}
}

func (h *AgentHarness) PromptFromTemplate(ctx context.Context, name string, args []string) (<-chan AgentEvent, error) {
	templates, err := prompt.LoadTemplates(ctx, h.env)
	if err != nil {
		return nil, err
	}
	for _, candidate := range templates {
		if candidate.Name == name {
			return h.Prompt(ctx, prompt.FormatPromptTemplateInvocation(candidate, args))
		}
	}
	return nil, &UnknownTemplateError{Name: name}
}
