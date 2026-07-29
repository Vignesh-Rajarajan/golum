package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Vignesh-Rajarajan/golum/internal/ui/styles"
	"github.com/Vignesh-Rajarajan/golum/pkg/applog"
	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/sanitize"
	"github.com/mattn/go-runewidth"
)

type Model struct {
	styles                styles.Styles
	client                *llm.Client
	cfg                   *config.Config
	ctxMgr                *contextmgr.ContextManager
	messages              []Message
	input                 textinput.Model
	spinner               spinner.Model
	viewport              viewport.Model
	scrollMode            bool
	streaming             bool
	ready                 bool
	width                 int
	height                int
	err                   error
	ctx                   context.Context
	streamReader          *streamReader
	streamCancel          context.CancelFunc
	streamCancelled       bool // user aborted; ignore late deltas until stream ends
	skipScrollAfterStream bool // avoid copy-mode after a cancelled stream
	selectionMode         bool
	selectionAnchor       selectionPos
	selectionCursor       selectionPos
	selectableText        string
	selectableLines       []selectableLine
	copyStatus            string
}

type selectionPos struct {
	Line int
	Col  int
}

type selectableLineKind int

const (
	selectableLineBlank selectableLineKind = iota
	selectableLineHeader
	selectableLineBody
	selectableLineMeta
)

type selectableLine struct {
	Text  string
	Start int
	End   int
	Role  MessageRole
	Kind  selectableLineKind
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

	vp := viewport.New()

	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	ctxMgr := contextmgr.NewContextManager(cfg, prompt.PromptConfig{CWD: cwd}, nil, nil)

	m := Model{
		styles:   s,
		client:   llm.NewClient(cfg),
		cfg:      cfg,
		ctxMgr:   ctxMgr,
		messages: make([]Message, 0),
		input:    ti,
		spinner:  sp,
		viewport: vp,
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

		viewportHeight := m.height - 8
		if viewportHeight < 5 {
			viewportHeight = 5
		}
		m.viewport.SetWidth(m.width)
		m.viewport.SetHeight(viewportHeight)
		m.syncViewportContent()

	case tea.KeyPressMsg:
		key := msg.Key()
		if m.scrollMode {
			if key.Mod&tea.ModCtrl != 0 {
				switch key.Code {
				case 'a':
					m.selectAll()
					m.copyStatus = ""
					m.syncViewportContent()
					return m, nil
				case 'c', 'd':
					return m, tea.Quit
				}
			}

			switch key.String() {
			case "i", "esc":
				m.exitScrollMode()
				cmds = append(cmds, m.input.Focus())
				return m, tea.Batch(cmds...)
			case "v":
				if len(m.selectableLines) > 0 {
					m.selectionMode = !m.selectionMode
					if !m.selectionMode {
						m.selectionAnchor = m.selectionCursor
					}
					m.copyStatus = ""
					m.syncViewportContent()
				}
				return m, nil
			case "h", "left":
				m.moveSelectionHorizontal(-1)
			case "l", "right":
				m.moveSelectionHorizontal(1)
			case "j", "down":
				m.moveSelectionVertical(1)
			case "k", "up":
				m.moveSelectionVertical(-1)
			case "home":
				m.selectionCursor.Col = 0
			case "end":
				if len(m.selectableLines) > 0 {
					m.selectionCursor.Col = lineRuneLen(m.selectableLines[m.selectionCursor.Line].Text)
				}
			case "y":
				selected := m.selectedText()
				if selected == "" {
					m.copyStatus = "No selection"
					m.syncViewportContent()
					return m, nil
				}
				m.copyStatus = "Copied selection"
				m.syncViewportContent()
				return m, tea.SetClipboard(selected)
			default:
				return m, nil
			}

			if !m.selectionMode {
				m.selectionAnchor = m.selectionCursor
			}
			m.copyStatus = ""
			m.syncViewportContent()
			return m, nil
		} else if key.String() == "esc" {
			if m.streaming {
				m.abortStream()
				m.syncViewportContent()
				return m, nil
			}
			m.enterScrollMode()
			return m, nil
		}

		if key.Mod&tea.ModCtrl != 0 {
			switch key.Code {
			case 'c', 'd':
				return m, tea.Quit
			case 'l':
				if !m.streaming && !m.scrollMode {
					m.messages = nil
					m.ctxMgr.Clear()
					m.copyStatus = ""
					m.syncViewportContent()
					return m, m.input.Focus()
				}
			}
		}

		if m.scrollMode {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			cmds = append(cmds, cmd)
			return m, tea.Batch(cmds...)
		}

		if key.Code == tea.KeyEnter {
			if !m.streaming && m.input.Value() != "" {
				text := strings.TrimSpace(m.input.Value())
				m.input.SetValue("")
				return m, m.sendMessage(text)
			}
		}

	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)

	case InputSubmitMsg:
		m.messages = append(m.messages, Message{
			Role:    RoleUser,
			Content: msg.Text,
		})
		m.ctxMgr.AddUserMessage(msg.Text)
		m.streaming = true
		m.syncViewportContent()
		m.viewport.GotoBottom()
		return m, m.startStream()

	case StreamMsg:
		if msg.Event.Type == llm.EventTypeContentDelta {
			if m.streamCancelled {
				m.syncViewportContent()
				if m.streamReader != nil {
					return m, m.streamReader.Read()
				}
				return m, nil
			}
			if len(m.messages) > 0 && m.messages[len(m.messages)-1].Role == RoleAssistant {
				m.messages[len(m.messages)-1].Content += msg.Event.Content
			} else {
				m.messages = append(m.messages, Message{
					Role:    RoleAssistant,
					Content: msg.Event.Content,
				})
			}
			m.viewport.GotoBottom()
		} else if msg.Event.Type == llm.EventTypeContentDone {
			if msg.Event.Cancelled {
				m.streaming = false
				m.streamCancelled = false
				m.skipScrollAfterStream = true
				if len(m.messages) > 0 && m.messages[len(m.messages)-1].Role == RoleAssistant {
					m.messages = m.messages[:len(m.messages)-1]
				}
				m.syncViewportContent()
				if m.streamReader != nil {
					return m, m.streamReader.Read()
				}
				return m, nil
			}
			if len(m.messages) > 0 && m.messages[len(m.messages)-1].Role == RoleAssistant {
				m.messages[len(m.messages)-1].Meta = msg.Event.Meta
				cleaned := sanitize.StripPseudoToolMarkup(m.messages[len(m.messages)-1].Content)
				m.messages[len(m.messages)-1].Content = cleaned
				m.ctxMgr.AddAssistantMessage(cleaned, nil)
			}
			m.streaming = false
			u := contextmgr.TokenUsageFromMeta(msg.Event.Meta)
			if u.TotalTokens > 0 || u.PromptTokens > 0 || u.CompletionTokens > 0 {
				m.ctxMgr.SetLatestUsage(u)
				m.ctxMgr.AddUsage(u)
			}
		} else if msg.Event.Type == llm.EventTypeError {
			applog.Printf("stream error: %v", msg.Event.Error)
			m.streaming = false
			m.messages = append(m.messages, Message{
				Role:    RoleError,
				Content: fmt.Sprintf("Error: %v", msg.Event.Error),
			})
			m.viewport.GotoBottom()
		}
		m.syncViewportContent()
		if m.streamReader != nil {
			return m, m.streamReader.Read()
		}
		return m, nil

	case StreamDoneMsg:
		if m.streaming {
			m.streaming = false
		}
		m.streamReader = nil
		m.streamCancelled = false
		m.releaseStreamCancel()
		m.syncViewportContent()
		longReply := m.viewport.TotalLineCount() > m.viewport.VisibleLineCount()
		if longReply && !m.skipScrollAfterStream {
			m.enterScrollMode()
			m.viewport.GotoBottom()
		} else if !m.input.Focused() {
			cmds = append(cmds, m.input.Focus())
		}
		m.skipScrollAfterStream = false

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

	header := styles.ApplyBoldForegroundGrad(&m.styles, "Golum Chat", m.styles.Primary, m.styles.Secondary)
	header = lipgloss.NewStyle().Padding(0, 1).Width(m.width).Render(header)
	b.WriteString(header)
	b.WriteString("\n")

	messagesView := m.renderMessagesView()
	shouldFollowBottom := m.streaming || m.viewport.AtBottom()
	m.viewport.SetContent(messagesView)
	if shouldFollowBottom {
		m.viewport.GotoBottom()
	}
	b.WriteString(m.viewport.View())
	b.WriteString("\n")

	var statusText string
	if m.streaming {
		statusText = m.styles.Chat.Thinking.Render("Generating response… · Esc cancels")
	} else if m.scrollMode {
		statusText = m.styles.Chat.Footer.Render("Scroll long reply · ←/→/↑/↓ or h/j/k/l: move | v: select | y: copy | Ctrl+A: all | i/Esc: type")
		if m.copyStatus != "" {
			statusText = m.styles.Chat.Footer.Render(m.copyStatus + " | v: select | y: copy | Ctrl+A: all | i/Esc: type")
		}
	} else {
		line := "Enter to send · Esc: scroll/copy · Ctrl+L: clear chat · mouse wheel · Ctrl+C quit"
		if hint := m.sessionFooterHint(); hint != "" {
			line = hint + " · " + line
		}
		statusText = m.styles.Chat.Footer.Render(line)
	}
	statusBar := m.styles.Chat.StatusBar.Width(m.width).Render(statusText)
	b.WriteString(statusBar)
	b.WriteString("\n\n")

	inputY := countLines(b.String()) - 1

	inputView := m.input.View()
	b.WriteString(inputView)

	v := tea.NewView(b.String())
	v.BackgroundColor = m.styles.BgBase
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion

	if m.input.Focused() {
		cursor := m.input.Cursor()
		if cursor != nil {
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

func (m *Model) renderMessagesView() string {
	if m.scrollMode {
		return m.renderSelectableMessagesView()
	}

	return m.renderStyledMessagesView()
}

func (m *Model) renderStyledMessagesView() string {
	contentWidth := m.width - 4
	var renderedMessages []string
	for _, msg := range m.messages {
		renderedMessages = append(renderedMessages, msg.Render(contentWidth, m.styles))
	}

	if m.showStreamingSpinner() {
		spinnerText := m.styles.Chat.Thinking.Render(m.spinner.View() + " Waiting for response")
		renderedMessages = append(renderedMessages, spinnerText)
	}

	return strings.Join(renderedMessages, "\n\n")
}

func (m *Model) renderSelectableMessagesView() string {
	m.syncSelectableBuffer()

	var rendered []string
	for i, line := range m.selectableLines {
		rendered = append(rendered, m.renderSelectableLine(line, i))
	}

	return strings.Join(rendered, "\n")
}

func (m *Model) renderSelectableLine(line selectableLine, lineIndex int) string {
	if line.Kind == selectableLineBlank {
		return ""
	}

	baseStyle := m.selectableLineStyle(line)
	textRunes := []rune(line.Text)
	lineLen := len(textRunes)

	if m.selectionMode {
		start, end := m.selectedRange()
		if end > line.Start && start < line.End {
			relStart := max(0, start-line.Start)
			relEnd := min(lineLen, end-line.Start)

			selectedStyle := lipgloss.NewStyle().
				Foreground(m.styles.BgBase).
				Background(m.styles.Primary)

			before := string(textRunes[:relStart])
			selected := string(textRunes[relStart:relEnd])
			after := string(textRunes[relEnd:])

			return baseStyle.Render(before) + selectedStyle.Render(selected) + baseStyle.Render(after)
		}
	}

	if lineIndex == m.selectionCursor.Line {
		cursorStyle := lipgloss.NewStyle().
			Foreground(m.styles.BgBase).
			Background(m.styles.Secondary)

		rel := clampInt(m.selectionCursor.Col, 0, lineLen)
		if rel < lineLen {
			before := string(textRunes[:rel])
			cursor := string(textRunes[rel : rel+1])
			after := string(textRunes[rel+1:])
			return baseStyle.Render(before) + cursorStyle.Render(cursor) + baseStyle.Render(after)
		}

		return baseStyle.Render(line.Text) + cursorStyle.Render(" ")
	}

	return baseStyle.Render(line.Text)
}

func (m Model) selectableLineStyle(line selectableLine) lipgloss.Style {
	switch line.Kind {
	case selectableLineHeader:
		switch line.Role {
		case RoleUser:
			return lipgloss.NewStyle().Bold(true).Foreground(m.styles.Primary)
		case RoleAssistant:
			return lipgloss.NewStyle().Bold(true).Foreground(m.styles.GreenDark)
		case RoleError:
			return lipgloss.NewStyle().Bold(true).Foreground(m.styles.Error)
		default:
			return lipgloss.NewStyle().Bold(true).Foreground(m.styles.FgMuted)
		}
	case selectableLineMeta:
		return lipgloss.NewStyle().Foreground(m.styles.FgSubtle)
	case selectableLineBody:
		switch line.Role {
		case RoleError:
			return lipgloss.NewStyle().Foreground(m.styles.Error)
		default:
			return lipgloss.NewStyle().Foreground(m.styles.FgBase)
		}
	default:
		return lipgloss.NewStyle()
	}
}

func (m *Model) syncViewportContent() {
	m.syncSelectableBuffer()
	if !m.ready || m.width <= 0 || m.viewport.Height() <= 0 {
		return
	}
	m.viewport.SetContent(m.renderMessagesView())
	if m.scrollMode {
		m.ensureSelectionVisible()
	}
}

func (m *Model) enterScrollMode() {
	m.scrollMode = true
	m.selectionMode = false
	m.copyStatus = ""
	m.input.Blur()
	m.syncSelectableBuffer()
	m.moveCursorToEnd()
	m.syncViewportContent()
}

func (m *Model) exitScrollMode() {
	m.scrollMode = false
	m.selectionMode = false
	m.copyStatus = ""
	m.selectionAnchor = m.selectionCursor
}

func (m *Model) moveCursorToEnd() {
	if len(m.selectableLines) == 0 {
		m.selectionCursor = selectionPos{}
		m.selectionAnchor = selectionPos{}
		return
	}

	lastLine := len(m.selectableLines) - 1
	lastCol := lineRuneLen(m.selectableLines[lastLine].Text)
	m.selectionCursor = selectionPos{Line: lastLine, Col: lastCol}
	m.selectionAnchor = m.selectionCursor
}

func (m *Model) moveSelectionHorizontal(delta int) {
	if len(m.selectableLines) == 0 || delta == 0 {
		return
	}

	pos := m.selectionCursor
	if delta < 0 {
		if pos.Col > 0 {
			pos.Col--
		} else if pos.Line > 0 {
			pos.Line--
			pos.Col = lineRuneLen(m.selectableLines[pos.Line].Text)
		}
	} else {
		lineLen := lineRuneLen(m.selectableLines[pos.Line].Text)
		if pos.Col < lineLen {
			pos.Col++
		} else if pos.Line < len(m.selectableLines)-1 {
			pos.Line++
			pos.Col = 0
		}
	}

	m.selectionCursor = pos
	m.clampSelection()
}

func (m *Model) moveSelectionVertical(delta int) {
	if len(m.selectableLines) == 0 || delta == 0 {
		return
	}

	pos := m.selectionCursor
	pos.Line = clampInt(pos.Line+delta, 0, len(m.selectableLines)-1)
	pos.Col = clampInt(pos.Col, 0, lineRuneLen(m.selectableLines[pos.Line].Text))
	m.selectionCursor = pos
	m.clampSelection()
}

func (m *Model) selectAll() {
	m.syncSelectableBuffer()
	if len(m.selectableLines) == 0 {
		return
	}

	m.selectionMode = true
	m.selectionAnchor = selectionPos{Line: 0, Col: 0}

	lastLine := len(m.selectableLines) - 1
	m.selectionCursor = selectionPos{
		Line: lastLine,
		Col:  lineRuneLen(m.selectableLines[lastLine].Text),
	}
	m.clampSelection()
}

func (m *Model) syncSelectableBuffer() {
	if m.width <= 0 {
		m.selectableText = ""
		m.selectableLines = nil
		return
	}

	contentWidth := max(10, m.width-4)

	type lineSeed struct {
		Text string
		Role MessageRole
		Kind selectableLineKind
	}

	var seeds []lineSeed
	for i, msg := range m.messages {
		seeds = append(seeds, lineSeed{
			Text: selectableHeader(msg.Role),
			Role: msg.Role,
			Kind: selectableLineHeader,
		})

		bodyLines := wrapSelectableText(msg.Content, contentWidth)
		if len(bodyLines) == 0 {
			bodyLines = []string{""}
		}
		for _, line := range bodyLines {
			seeds = append(seeds, lineSeed{
				Text: line,
				Role: msg.Role,
				Kind: selectableLineBody,
			})
		}

		if meta := selectableMeta(msg.Meta); meta != "" {
			for _, line := range wrapSelectableText(meta, contentWidth) {
				seeds = append(seeds, lineSeed{
					Text: line,
					Role: msg.Role,
					Kind: selectableLineMeta,
				})
			}
		}

		if i < len(m.messages)-1 {
			seeds = append(seeds, lineSeed{Kind: selectableLineBlank})
		}
	}

	if m.showStreamingSpinner() {
		if len(seeds) > 0 {
			seeds = append(seeds, lineSeed{Kind: selectableLineBlank})
		}
		seeds = append(seeds, lineSeed{
			Text: "Assistant",
			Role: RoleAssistant,
			Kind: selectableLineHeader,
		})
		seeds = append(seeds, lineSeed{
			Text: "Waiting for response",
			Role: RoleAssistant,
			Kind: selectableLineMeta,
		})
	}

	var b strings.Builder
	lines := make([]selectableLine, 0, len(seeds))
	offset := 0
	for i, seed := range seeds {
		start := offset
		end := start + lineRuneLen(seed.Text)
		lines = append(lines, selectableLine{
			Text:  seed.Text,
			Start: start,
			End:   end,
			Role:  seed.Role,
			Kind:  seed.Kind,
		})
		b.WriteString(seed.Text)
		offset = end
		if i < len(seeds)-1 {
			b.WriteByte('\n')
			offset++
		}
	}

	m.selectableText = b.String()
	m.selectableLines = lines
	m.clampSelection()
}

func (m *Model) clampSelection() {
	if len(m.selectableLines) == 0 {
		m.selectionCursor = selectionPos{}
		m.selectionAnchor = selectionPos{}
		m.selectionMode = false
		return
	}

	m.selectionCursor.Line = clampInt(m.selectionCursor.Line, 0, len(m.selectableLines)-1)
	m.selectionAnchor.Line = clampInt(m.selectionAnchor.Line, 0, len(m.selectableLines)-1)

	m.selectionCursor.Col = clampInt(m.selectionCursor.Col, 0, lineRuneLen(m.selectableLines[m.selectionCursor.Line].Text))
	m.selectionAnchor.Col = clampInt(m.selectionAnchor.Col, 0, lineRuneLen(m.selectableLines[m.selectionAnchor.Line].Text))
}

func (m *Model) ensureSelectionVisible() {
	if len(m.selectableLines) == 0 {
		return
	}

	line := clampInt(m.selectionCursor.Line, 0, len(m.selectableLines)-1)
	col := clampInt(m.selectionCursor.Col, 0, lineRuneLen(m.selectableLines[line].Text))
	m.viewport.EnsureVisible(line, col, col+1)
}

func (m Model) selectedRange() (int, int) {
	if !m.selectionMode || len(m.selectableLines) == 0 {
		return 0, 0
	}

	start := m.absoluteOffset(m.selectionAnchor)
	end := m.absoluteOffset(m.selectionCursor)
	if start > end {
		start, end = end, start
	}
	return start, end
}

func (m Model) selectedText() string {
	start, end := m.selectedRange()
	if start == end {
		return ""
	}

	runes := []rune(m.selectableText)
	start = clampInt(start, 0, len(runes))
	end = clampInt(end, 0, len(runes))
	return string(runes[start:end])
}

func (m Model) absoluteOffset(pos selectionPos) int {
	if len(m.selectableLines) == 0 {
		return 0
	}

	line := clampInt(pos.Line, 0, len(m.selectableLines)-1)
	col := clampInt(pos.Col, 0, lineRuneLen(m.selectableLines[line].Text))
	return m.selectableLines[line].Start + col
}

func selectableHeader(role MessageRole) string {
	switch role {
	case RoleUser:
		return "You"
	case RoleAssistant:
		return "Assistant"
	case RoleError:
		return "Error"
	case RoleSystem:
		return "System"
	default:
		return "Message"
	}
}

func selectableMeta(meta map[string]string) string {
	if len(meta) == 0 {
		return ""
	}

	var metaParts []string
	if pt, ok := meta["prompt_tokens"]; ok {
		metaParts = append(metaParts, "Prompt: "+pt)
	}
	if ct, ok := meta["completion_tokens"]; ok {
		metaParts = append(metaParts, "Completion: "+ct)
	}
	if tt, ok := meta["total_tokens"]; ok {
		metaParts = append(metaParts, "Total: "+tt)
	}
	return strings.Join(metaParts, " | ")
}

func wrapSelectableText(text string, width int) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text == "" {
		return nil
	}

	var wrapped []string
	for _, rawLine := range strings.Split(text, "\n") {
		if rawLine == "" {
			wrapped = append(wrapped, "")
			continue
		}

		var current []rune
		currentWidth := 0
		for _, r := range rawLine {
			rw := runewidth.RuneWidth(r)
			if rw == 0 {
				rw = 1
			}
			if currentWidth+rw > width && len(current) > 0 {
				wrapped = append(wrapped, string(current))
				current = current[:0]
				currentWidth = 0
			}
			current = append(current, r)
			currentWidth += rw
		}
		wrapped = append(wrapped, string(current))
	}
	return wrapped
}

func lineRuneLen(s string) int {
	return len([]rune(s))
}

func clampInt(v, minV, maxV int) int {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}

func (m Model) showStreamingSpinner() bool {
	if !m.streaming {
		return false
	}
	if len(m.messages) == 0 {
		return true
	}
	last := m.messages[len(m.messages)-1]
	if last.Role != RoleAssistant {
		return true
	}
	return strings.TrimSpace(last.Content) == ""
}

func (m *Model) sendMessage(text string) tea.Cmd {
	return func() tea.Msg {
		return InputSubmitMsg{Text: text}
	}
}

func (m *Model) releaseStreamCancel() {
	if m.streamCancel != nil {
		m.streamCancel()
		m.streamCancel = nil
	}
}

// abortStream cancels the in-flight API request, drops any partial assistant bubble, and
// suppresses auto-entering scroll/copy mode when the stream ends.
func (m *Model) abortStream() {
	m.skipScrollAfterStream = true
	if m.streamCancel != nil {
		m.streamCancel()
	}
	m.streamCancelled = true
	m.streaming = false
	if len(m.messages) == 0 {
		return
	}
	last := m.messages[len(m.messages)-1]
	if last.Role == RoleAssistant {
		m.messages = m.messages[:len(m.messages)-1]
	}
}

func (m *Model) startStream() tea.Cmd {
	m.releaseStreamCancel()
	m.streamCancelled = false
	m.skipScrollAfterStream = false

	history := m.ctxMgr.ChatCompletionMessages()

	timeout := 10 * time.Minute
	if m.cfg != nil {
		timeout = m.cfg.StreamTimeoutOrDefault()
	}
	applog.Printf("ui: startStream history_msgs=%d timeout=%s", len(history), timeout)

	opts := llm.ChatCompletionOptions{
		Stream:     true,
		MaxRetries: 3,
		Timeout:    timeout,
	}

	streamCtx, cancel := context.WithCancel(m.ctx)
	m.streamCancel = cancel

	events := m.client.ChatCompletion(streamCtx, history, opts)
	m.streamReader = &streamReader{events: events}
	return m.streamReader.Read()
}

func (m Model) sessionFooterHint() string {
	if m.ctxMgr == nil {
		return ""
	}
	tu := m.ctxMgr.TotalUsage
	var b strings.Builder
	if tu.TotalTokens > 0 {
		fmt.Fprintf(&b, "session Σ %d tok", tu.TotalTokens)
	}
	if m.ctxMgr.NeedsCompression() {
		if b.Len() > 0 {
			b.WriteString(" · ")
		}
		b.WriteString("last response >80% context — summarize or start fresh")
	}
	return b.String()
}
