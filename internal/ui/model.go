package ui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Vignesh-Rajarajan/golum/internal/ui/styles"
	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/sashabaranov/go-openai"
)

type Model struct {
	styles       styles.Styles
	client       *llm.Client
	messages     []Message
	input        textinput.Model
	spinner      spinner.Model
	streaming    bool
	ready        bool
	width        int
	height       int
	err          error
	ctx          context.Context
	streamReader *streamReader
}

func NewModel(cfg *config.Config) Model {
	s := styles.DefaultStyles()

	ti := textinput.New()
	ti.SetStyles(textinput.DefaultDarkStyles())
	ti.Placeholder = "Type your message..."
	ti.CharLimit = 0
	ti.Prompt = "> "
	ti.SetVirtualCursor(false)

	sp := spinner.New(spinner.WithSpinner(spinner.Points), spinner.WithStyle(s.Chat.Spinner))

	m := Model{
		styles:   s,
		client:   llm.NewClient(cfg),
		messages: make([]Message, 0),
		input:    ti,
		spinner:  sp,
		ctx:      context.Background(),
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.input.Focus(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true

		inputWidth := m.width - 4
		if inputWidth < 20 {
			inputWidth = 20
		}
		m.input.SetWidth(inputWidth)
		if !m.input.Focused() {
			cmds = append(cmds, m.input.Focus())
		}

	case tea.KeyPressMsg:
		key := msg.Key()
		if key.Mod&tea.ModCtrl != 0 {
			switch key.Code {
			case 'c', 'd':
				return m, tea.Quit
			}
		}
		if key.Code == tea.KeyEnter {
			if !m.streaming && m.input.Value() != "" {
				text := strings.TrimSpace(m.input.Value())
				m.input.SetValue("")
				return m, m.sendMessage(text)
			}
		}

	case InputSubmitMsg:
		m.messages = append(m.messages, Message{
			Role:    RoleUser,
			Content: msg.Text,
		})
		m.streaming = true
		return m, m.startStream(msg.Text)

	case StreamMsg:
		if msg.Event.Type == llm.EventTypeContentDelta {
			if len(m.messages) > 0 && m.messages[len(m.messages)-1].Role == RoleAssistant {
				m.messages[len(m.messages)-1].Content += msg.Event.Content
			} else {
				m.messages = append(m.messages, Message{
					Role:    RoleAssistant,
					Content: msg.Event.Content,
				})
			}
		} else if msg.Event.Type == llm.EventTypeContentDone {
			if len(m.messages) > 0 && m.messages[len(m.messages)-1].Role == RoleAssistant {
				m.messages[len(m.messages)-1].Meta = msg.Event.Meta
			}
		} else if msg.Event.Type == llm.EventTypeError {
			m.messages = append(m.messages, Message{
				Role:    RoleError,
				Content: fmt.Sprintf("Error: %v", msg.Event.Error),
			})
		}
		if m.streamReader != nil {
			return m, m.streamReader.Read()
		}
		return m, nil

	case StreamDoneMsg:
		m.streaming = false
		m.streamReader = nil
		if !m.input.Focused() {
			cmds = append(cmds, m.input.Focus())
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m Model) View() tea.View {
	if !m.ready {
		return tea.NewView("Loading...")
	}

	if m.width < 20 || m.height < 10 {
		return tea.NewView(m.styles.WindowTooSmall.Render("Window too small"))
	}

	var b strings.Builder
	linesWritten := 0

	header := styles.ApplyBoldForegroundGrad(&m.styles, "Golum Chat", m.styles.Primary, m.styles.Secondary)
	header = lipgloss.NewStyle().Padding(0, 1).Width(m.width).Render(header)
	b.WriteString(header)
	b.WriteString("\n")
	linesWritten = 1

	contentWidth := m.width - 4

	var renderedMessages []string
	for _, msg := range m.messages {
		renderedMessages = append(renderedMessages, msg.Render(contentWidth, m.styles))
	}

	if m.streaming && (len(m.messages) == 0 || m.messages[len(m.messages)-1].Role != RoleAssistant) {
		spinnerText := m.styles.Chat.Thinking.Render(m.spinner.View() + " Thinking...")
		renderedMessages = append(renderedMessages, spinnerText)
	}

	messagesView := strings.Join(renderedMessages, "\n\n")
	b.WriteString(messagesView)
	linesWritten += countLines(messagesView)
	b.WriteString("\n\n")
	linesWritten += 2

	var statusText string
	if m.streaming {
		statusText = m.styles.Chat.Thinking.Render("Generating response...")
	} else {
		statusText = m.styles.Chat.Footer.Render("Press Enter to send, Ctrl+C to quit")
	}
	statusBar := m.styles.Chat.StatusBar.Width(m.width).Render(statusText)
	b.WriteString(statusBar)
	b.WriteString("\n\n")
	linesWritten += 3

	// Determine the input's starting row based on the number of newline
	// characters written so far. This directly corresponds to the row index
	// on which the input will begin.
	inputY := countLines(b.String()) - 1

	inputView := m.input.View()
	b.WriteString(inputView)

	v := tea.NewView(b.String())
	v.BackgroundColor = m.styles.BgBase
	v.AltScreen = true

	if m.input.Focused() {
		cursor := m.input.Cursor()
		if cursor != nil {
			// textinput's Cursor() returns a cursor positioned relative to the
			// input view itself (row 0). Offset its row by the number of
			// lines already written so that the real cursor lines up with
			// the rendered input.
			cursor.Y += inputY
			v.Cursor = cursor
		}
	}
	return v
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func (m *Model) sendMessage(text string) tea.Cmd {
	return func() tea.Msg {
		return InputSubmitMsg{Text: text}
	}
}

func (m *Model) startStream(text string) tea.Cmd {
	history := m.buildHistory()
	history = append(history, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: text,
	})

	opts := llm.ChatCompletionOptions{
		Stream:     true,
		MaxRetries: 3,
	}

	events := m.client.ChatCompletion(m.ctx, history, opts)
	m.streamReader = &streamReader{events: events}
	return m.streamReader.Read()
}

func (m *Model) buildHistory() []openai.ChatCompletionMessage {
	var history []openai.ChatCompletionMessage
	for _, msg := range m.messages {
		var role string
		switch msg.Role {
		case RoleUser:
			role = openai.ChatMessageRoleUser
		case RoleAssistant:
			role = openai.ChatMessageRoleAssistant
		case RoleSystem:
			role = openai.ChatMessageRoleSystem
		default:
			continue
		}
		history = append(history, openai.ChatCompletionMessage{
			Role:    role,
			Content: msg.Content,
		})
	}
	return history
}
