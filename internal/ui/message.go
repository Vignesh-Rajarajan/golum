package ui

import (
	"fmt"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/Vignesh-Rajarajan/golum/internal/ui/styles"
	"github.com/Vignesh-Rajarajan/golum/pkg/sanitize"
)

type MessageRole string

const (
	RoleUser       MessageRole = "user"
	RoleAssistant  MessageRole = "assistant"
	RoleSystem     MessageRole = "system"
	RoleError      MessageRole = "error"
	RoleToolCall   MessageRole = "tool_call"
	RoleToolResult MessageRole = "tool_result"
	RoleThinking   MessageRole = "thinking"
)

type Message struct {
	Role    MessageRole
	Content string
	Meta    map[string]string
	IsError bool
	Collapsed bool // for thinking blocks
}

func (m Message) Render(width int, s styles.Styles) string {
	var content string
	var rendered string

	content = m.Content
	if m.Role == RoleAssistant {
		content = sanitize.StripPseudoToolMarkup(content)
	}

	switch m.Role {
	case RoleUser:
		header := styles.ApplyBoldForegroundGrad(&s, "You", s.Primary, s.Secondary)
		contentStyle := s.Chat.UserMessage.Width(width - 2)
		rendered = lipgloss.JoinVertical(
			lipgloss.Left,
			header,
			contentStyle.Render(content),
		)
	case RoleError:
		rendered = s.Chat.ErrorMessage.Render(content)
	case RoleToolCall:
		icon := styles.ToolPending
		line := fmt.Sprintf("%s %s", icon, content)
		rendered = lipgloss.NewStyle().Foreground(s.FgMuted).Render(line)
	case RoleToolResult:
		icon := styles.ToolSuccess
		if m.IsError {
			icon = styles.ToolError
		}
		header := lipgloss.NewStyle().Foreground(s.FgMuted).Render(fmt.Sprintf("%s %s", icon, firstLine(content)))
		body := content
		if idx := strings.Index(content, "\n"); idx >= 0 {
			body = content[idx+1:]
		} else {
			body = ""
		}
		if strings.TrimSpace(body) == "" {
			rendered = header
		} else {
			border := lipgloss.NewStyle().
				BorderLeft(true).
				BorderStyle(lipgloss.NormalBorder()).
				BorderForeground(s.Border).
				PaddingLeft(1).
				Width(width - 2).
				Foreground(s.FgBase)
			rendered = lipgloss.JoinVertical(lipgloss.Left, header, border.Render(strings.TrimRight(body, "\n")))
		}
	case RoleThinking:
		if m.Collapsed {
			rendered = s.Chat.Thinking.Render("⋯ thinking (press t to expand)")
		} else {
			header := s.Chat.Thinking.Render("Thinking:")
			body := s.Chat.Thinking.Width(width - 2).Render(content)
			rendered = lipgloss.JoinVertical(lipgloss.Left, header, body)
		}
	default:
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

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
