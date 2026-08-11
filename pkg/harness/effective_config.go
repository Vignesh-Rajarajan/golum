package harness

import (
	"fmt"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
)

type EffectiveConfig struct {
	Model, ThinkingLevel string
	ActiveTools          []string
}

func DeriveEffectiveConfig(sess session.Session, defaults EffectiveConfig) EffectiveConfig {
	path, err := sess.GetPathToRoot(sess.Leaf())
	if err != nil {
		return defaults
	}
	out := defaults
	for _, e := range path {
		switch e.Kind {
		case session.EntryModelChange:
			out.Model = e.Content
		case session.EntryThinkingLevelChange:
			out.ThinkingLevel = e.Content
		case session.EntryActiveToolsChange:
			out.ActiveTools = stringsFromMeta(e.Meta["tools"])
		case session.EntryAssistantMessage:
			if model, _ := e.Meta["model"].(string); model != "" {
				out.Model = model
			}
		}
	}
	return out
}

func stringsFromMeta(v any) []string {
	switch values := v.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if s, ok := value.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func (h *AgentHarness) EffectiveConfig() EffectiveConfig {
	return DeriveEffectiveConfig(h.session, EffectiveConfig{
		Model: h.model, ThinkingLevel: h.thinkingLevel, ActiveTools: h.registry.Names(),
	})
}

func (h *AgentHarness) SetModel(model string) error {
	if model == "" {
		return fmt.Errorf("model required")
	}
	_, err := h.session.AppendModelChange(model)
	return err
}

func (h *AgentHarness) Model() string { return h.EffectiveConfig().Model }

func (h *AgentHarness) SetThinkingLevel(level string) error {
	if level == "" {
		return fmt.Errorf("thinking level required")
	}
	_, err := h.session.AppendThinkingLevelChange(level)
	return err
}

func (h *AgentHarness) ThinkingLevel() string { return h.EffectiveConfig().ThinkingLevel }

func (h *AgentHarness) SetActiveTools(names []string) error {
	for _, name := range names {
		if _, ok := h.registry.Get(name); !ok {
			return fmt.Errorf("unknown tool %q", name)
		}
	}
	_, err := h.session.AppendActiveToolsChange(names)
	return err
}

func (h *AgentHarness) ActiveTools() []string {
	return h.EffectiveConfig().ActiveTools
}
