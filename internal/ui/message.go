package ui

import (
	"strings"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/Vignesh-Rajarajan/golum/internal/ui/styles"
	"github.com/Vignesh-Rajarajan/golum/pkg/sanitize"
)

type MessageRole string

const (
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleSystem    MessageRole = "system"
	RoleError     MessageRole = "error"
)

type Message struct {
	Role    MessageRole
	Content string
	Meta    map[string]string
}

func (m Message) Render(width int, s styles.Styles) string {
	var content string
	var rendered string

	content = m.Content
	if m.Role == RoleAssistant {
		content = sanitize.StripPseudoToolMarkup(content)
	}

	if m.Role == RoleUser {
		header := styles.ApplyBoldForegroundGrad(&s, "You", s.Primary, s.Secondary)
		contentStyle := s.Chat.UserMessage.Width(width - 2)
		rendered = lipgloss.JoinVertical(
			lipgloss.Left,
			header,
			contentStyle.Render(content),
		)
	} else if m.Role == RoleError {
		rendered = s.Chat.ErrorMessage.Render(content)
	} else {
		header := styles.ApplyBoldForegroundGrad(&s, "Assistant", s.GreenDark, s.Tertiary)

		md, err := glamour.NewTermRenderer(
			glamour.WithStyles(s.Markdown),
			glamour.WithWordWrap(width-4),
		)
		if err == nil {
			out, err := md.Render(content)
			if err == nil {
				content = out
			}
		}

		contentStyle := s.Chat.AssistantMessage.Width(width - 2)
		rendered = lipgloss.JoinVertical(
			lipgloss.Left,
			header,
			contentStyle.Render(strings.TrimSpace(content)),
		)

		if m.Meta != nil && len(m.Meta) > 0 {
			metaStyle := s.Subtle.PaddingLeft(1)
			var metaParts []string
			if pt, ok := m.Meta["prompt_tokens"]; ok {
				metaParts = append(metaParts, "Prompt: "+pt)
			}
			if ct, ok := m.Meta["completion_tokens"]; ok {
				metaParts = append(metaParts, "Completion: "+ct)
			}
			if tt, ok := m.Meta["total_tokens"]; ok {
				metaParts = append(metaParts, "Total: "+tt)
			}
			if len(metaParts) > 0 {
				rendered = lipgloss.JoinVertical(
					lipgloss.Left,
					rendered,
					metaStyle.Render(strings.Join(metaParts, " | ")),
				)
			}
		}
	}

	return rendered
}
