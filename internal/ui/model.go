package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Vignesh-Rajarajan/golum/internal/ui/styles"
	"github.com/Vignesh-Rajarajan/golum/pkg/applog"
	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/memory"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/skill"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
	"github.com/mattn/go-runewidth"
)

type Model struct {
	styles                styles.Styles
	cfg                   *config.Config
	promptCfg             prompt.PromptConfig
	store                 *session.SQLiteStore
	memory                *memory.Store
	active                *sessionRef
	harness               *harness.AgentHarness
	approvals             *approvalBroker
	messages              []Message
	input                 textinput.Model
	spinner               spinner.Model
	viewport              viewport.Model
	scrollMode            bool
	streaming             bool
	awaitingApproval      bool
	approvalSummary       string
	compacting            bool
	thinkingExpanded      bool
	ready                 bool
	width                 int
	height                int
	err                   error
	ctx                   context.Context
	eventReader           *agentEventReader
	streamCancelled       bool
	skipScrollAfterStream bool
	selectionMode         bool
	selectionAnchor       selectionPos
	selectionCursor       selectionPos
	selectableText        string
	selectableLines       []selectableLine
	copyStatus            string
	todosPanel            string
	picker                pickerState
	suggestIdx            int
	lastInputValue        string
	viewportBase          int
	queued                []session.ProvisionedEntry
	templates             []prompt.Template
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

// Options configures the initial model.
type Options struct {
	// ResumeSessionID, when set, reopens that saved session instead of
	// starting a new one.
	ResumeSessionID string
}

func NewModel(cfg *config.Config, opts Options) Model {
	s := styles.DefaultStyles()

	ti := textinput.New()
	tiStyles := textinput.DefaultDarkStyles()
	tiStyles.Focused.Prompt = lipgloss.NewStyle().Foreground(s.Primary)
	tiStyles.Focused.Text = lipgloss.NewStyle().Foreground(s.FgBase)
	tiStyles.Focused.Placeholder = lipgloss.NewStyle().Foreground(s.FgSubtle)
	tiStyles.Blurred.Prompt = lipgloss.NewStyle().Foreground(s.FgSubtle)
	tiStyles.Blurred.Text = lipgloss.NewStyle().Foreground(s.FgMuted)
	tiStyles.Blurred.Placeholder = lipgloss.NewStyle().Foreground(s.FgSubtle)
	tiStyles.Cursor.Color = s.Primary
	ti.SetStyles(tiStyles)
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
	env, err := execenv.NewOsExecutionEnv(cwd)
	if err != nil {
		applog.Printf("ui: execenv: %v", err)
		env, _ = execenv.NewOsExecutionEnv(".")
	}
	reg, todos := tool.DefaultRegistry(nil)

	// Procedural memory: project conventions the prompt has always claimed to
	// follow (AGENTS.md) plus cross-project user instructions.
	promptCfg := prompt.PromptConfig{CWD: cwd}
	loadedSkills, _ := skill.LoadSkills(context.Background(), env)
	if len(loadedSkills) > 0 {
		promptCfg.SkillsSection = skill.FormatSkillsSection(loadedSkills)
	}
	if dev, err := memory.LoadProjectInstructions(cwd); err == nil && dev != "" {
		promptCfg.DeveloperInstructions = dev
	}
	if usr, err := memory.LoadUserInstructions(); err == nil && usr != "" {
		promptCfg.UserInstructions = usr
	}

	broker := newApprovalBroker()
	active := &sessionRef{}

	// Open the database before creating the session: the memory tool has to be
	// registered while the tool list is still being assembled, because
	// NewContextManager freezes that list into the system prompt.
	store := openSessionDB(cfg, promptCfg)
	var memStore *memory.Store
	if store != nil {
		memStore = memory.NewStore(store.DB())
		tool.RegisterMemory(reg, memStore, active.id)

		// Remembered user facts populate the system prompt's memory section,
		// which until now had no source and was therefore never rendered.
		if recs, err := memStore.ListScoped(context.Background(),
			memory.TierProcedural, memory.ScopeUser, 50); err == nil {
			store.SetUserMemory(memory.FormatForPrompt(recs))
		}
	}
	// Set the tool list only once the registry is final — the system prompt's
	// tool guidance is derived from it.
	if store != nil {
		store.SetTools(reg.AsLLMTools())
	}

	sess := startSession(store, opts.ResumeSessionID)
	active.set(sess)

	h, err := harness.NewAgentHarness(harness.HarnessConfig{
		Config:    cfg,
		Env:       env,
		Registry:  reg,
		Todos:     todos,
		Session:   sess,
		Approvals: broker,
		Memory:    memStore,
		PromptCfg: promptCfg,
		Skills:    loadedSkills,
	})
	if err != nil {
		applog.Printf("ui: harness: %v", err)
	}

	m := Model{
		styles:    s,
		cfg:       cfg,
		promptCfg: promptCfg,
		store:     store,
		memory:    memStore,
		active:    active,
		harness:   h,
		approvals: broker,
		messages:  transcriptFromSession(sess),
		input:     ti,
		spinner:   sp,
		viewport:  vp,
		ctx:       context.Background(),
	}
	m.templates, _ = prompt.LoadTemplates(context.Background(), env)
	if h != nil {
		if open, openErr := sess.FindOpenOperations("main", 2); openErr == nil && len(open) == 1 {
			m.messages = append(m.messages, Message{
				Role:    RoleSystem,
				Content: "A suspended operation was recovered. Use /resume-run to continue it or /abort-run to discard it.",
			})
		}
	}
	return m
}

// sessionRef is a mutable handle to the current session, so tools registered
// before the session exists (and kept across resume/fork) always report the
// session that is actually active.
type sessionRef struct {
	mu sync.Mutex
	s  session.Session
}

func (r *sessionRef) set(s session.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.s = s
}

func (r *sessionRef) id() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.s == nil {
		return ""
	}
	return r.s.ID()
}

// openSessionDB opens the SQLite database and migrates any legacy JSONL
// sessions. A nil return is tolerated: the harness falls back to an in-memory
// session so the TUI still runs without persistence.
func openSessionDB(cfg *config.Config, promptCfg prompt.PromptConfig) *session.SQLiteStore {
	path, err := session.DefaultDBPath()
	if err != nil {
		applog.Printf("ui: session db path: %v", err)
		return nil
	}
	store, err := session.OpenSQLiteStore(path, cfg, promptCfg, nil)
	if err != nil {
		applog.Printf("ui: open session db: %v", err)
		return nil
	}
	if dir, err := session.DefaultSessionsDir(); err == nil {
		if n, err := session.MigrateJSONL(context.Background(), store, dir); err != nil {
			applog.Printf("ui: migrate jsonl sessions: %v", err)
		} else if n > 0 {
			applog.Printf("ui: migrated %d legacy jsonl session(s) into sqlite", n)
		}
	}
	return store
}

// startSession resumes resumeID when given, otherwise creates a new session.
func startSession(store *session.SQLiteStore, resumeID string) session.Session {
	if store == nil {
		return nil
	}
	if resumeID != "" {
		sess, err := store.Open(context.Background(), resumeID)
		if err == nil {
			applog.Printf("ui: resumed session %s", resumeID)
			return sess
		}
		// Fall through to a new session rather than refusing to start.
		applog.Printf("ui: resume %s failed: %v", resumeID, err)
	}
	sess, err := store.Create(context.Background())
	if err != nil {
		applog.Printf("ui: create session: %v", err)
		return nil
	}
	return sess
}

// transcriptFromSession rebuilds the visible transcript for a resumed session
// so the user sees their history, not an empty screen with hidden context.
func transcriptFromSession(sess session.Session) []Message {
	if sess == nil {
		return nil
	}
	var out []Message
	for _, e := range sess.ContextEntries() {
		switch e.Kind {
		case session.EntryUserMessage:
			out = append(out, Message{Role: RoleUser, Content: e.Content})
		case session.EntryAssistantMessage:
			if strings.TrimSpace(e.Content) != "" {
				out = append(out, Message{Role: RoleAssistant, Content: e.Content})
			}
		case session.EntryToolResult:
			out = append(out, Message{Role: RoleToolResult, Content: firstLine(e.Content)})
		case session.EntryCompaction:
			out = append(out, Message{
				Role:    RoleSystem,
				Content: "— earlier turns compacted —",
			})
		}
	}
	return out
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.input.Focus(),
		m.autoIndexCmd(),
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
		if viewportHeight < minViewportRows {
			viewportHeight = minViewportRows
		}
		m.viewport.SetWidth(m.width)
		m.viewport.SetHeight(viewportHeight)
		// Remember the unreduced height; View() subtracts the suggestion
		// popup from this base each frame.
		m.viewportBase = viewportHeight
		m.syncViewportContent()

	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)

	case tea.KeyPressMsg:
		key := msg.Key()

		// Session browser owns all input while open.
		if cmd, handled := m.handlePickerKey(key); handled {
			m.syncViewportContent()
			if cmd != nil {
				return m, cmd
			}
			return m, nil
		}

		// Approval gate — y/n only while awaiting approval
		if m.awaitingApproval {
			switch key.String() {
			case "y", "Y":
				m.awaitingApproval = false
				m.approvalSummary = ""
				m.approvals.Decide(true)
				m.syncViewportContent()
				return m, nil
			case "n", "N":
				m.awaitingApproval = false
				m.approvalSummary = ""
				m.approvals.Decide(false)
				m.syncViewportContent()
				return m, nil
			case "esc":
				if m.streaming {
					m.abortStream()
					m.syncViewportContent()
				}
				return m, nil
			default:
				return m, nil
			}
		}

		if key.String() == "t" && m.scrollMode && !m.streaming {
			m.thinkingExpanded = !m.thinkingExpanded
			for i := range m.messages {
				if m.messages[i].Role == RoleThinking {
					m.messages[i].Collapsed = !m.thinkingExpanded
				}
			}
			m.syncViewportContent()
			return m, nil
		}

		// Slash-command suggestions — only while a command name is being typed
		// (no space yet) and not mid-turn, so Enter/Tab/↑/↓ don't collide with
		// their normal jobs once the user has moved on to arguments or a reply.
		if !m.scrollMode && !m.streaming {
			if suggestions := m.slashSuggestions(); len(suggestions) > 0 {
				// Match key.Code as well as the string form (as the Enter handling
				// below already does), rather than relying on String() alone.
				if key.Code == tea.KeyDown || key.String() == "ctrl+n" {
					m.suggestIdx = (m.suggestIdx + 1) % len(suggestions)
					return m, nil
				}
				if key.Code == tea.KeyUp || key.String() == "ctrl+p" {
					m.suggestIdx = (m.suggestIdx - 1 + len(suggestions)) % len(suggestions)
					return m, nil
				}
				switch key.String() {
				case "tab":
					chosen := m.selectedSuggestion(suggestions)
					m.input.SetValue("/" + chosen.Name + " ")
					m.input.CursorEnd()
					m.suggestIdx = 0
					return m, nil
				case "esc":
					m.input.SetValue("")
					m.suggestIdx = 0
					return m, nil
				}
				if key.Code == tea.KeyEnter {
					chosen := m.selectedSuggestion(suggestions)
					m.input.SetValue("")
					m.suggestIdx = 0
					return m, m.sendMessage("/" + chosen.Name)
				}
			}
		}

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
				// Same effect as /clear.
				if !m.streaming && !m.scrollMode {
					if m.harness != nil {
						m.startNewSession()
					} else {
						m.messages = nil
						m.todosPanel = ""
					}
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
			if m.input.Value() != "" {
				text := strings.TrimSpace(m.input.Value())
				m.input.SetValue("")
				if m.streaming {
					if m.harness == nil {
						m.messages = append(m.messages, Message{
							Role: RoleError, Content: "Harness is not initialized.",
						})
						return m, nil
					}
					if strings.HasPrefix(text, "/") {
						return m, m.sendMessage(text)
					}
					h := m.harness
					ctx := m.ctx
					return m, func() tea.Msg {
						entry, err := h.Steer(ctx, text)
						return SteerQueuedMsg{Entry: entry, Err: err}
					}
				}
				return m, m.sendMessage(text)
			}
		}

	case InputSubmitMsg:
		if cmd, handled := m.handleSlashCommand(msg.Text); handled {
			m.syncViewportContent()
			m.viewport.GotoBottom()
			return m, cmd
		}
		m.messages = append(m.messages, Message{
			Role:    RoleUser,
			Content: msg.Text,
		})
		m.streaming = true
		m.syncViewportContent()
		m.viewport.GotoBottom()
		return m, m.startAgent(msg.Text)

	case SteerQueuedMsg:
		if msg.Err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: msg.Err.Error()})
		} else {
			m.queued = append(m.queued, msg.Entry)
		}
		m.syncViewportContent()
		return m, nil

	case ResumeRunDoneMsg:
		m.streaming = false
		if msg.Err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: msg.Err.Error()})
		} else {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Suspended operation completed."})
		}
		m.syncViewportContent()
		return m, nil

	case SessionsLoadedMsg:
		m.picker.loading = false
		m.picker.sessions = msg.Sessions
		m.picker.err = msg.Err
		m.picker.cursor = 0
		m.syncViewportContent()

	case SessionOpenedMsg:
		if msg.Err != nil {
			m.picker.err = msg.Err
			m.picker.status = ""
			m.syncViewportContent()
			return m, nil
		}
		m.adoptSession(msg.Session)
		cmds = append(cmds, m.closePicker())
		m.syncViewportContent()
		m.viewport.GotoBottom()
		return m, tea.Batch(cmds...)

	case ReindexDoneMsg:
		if msg.Err != nil {
			m.messages = append(m.messages, Message{
				Role:    RoleError,
				Content: "Indexing failed: " + msg.Err.Error(),
			})
		} else {
			m.messages = append(m.messages, Message{
				Role:    RoleSystem,
				Content: fmt.Sprintf("Indexed %d entries of project structure.", msg.Count),
			})
		}
		m.syncViewportContent()
		m.viewport.GotoBottom()

	case MemoryReportMsg:
		if msg.Err != nil {
			m.messages = append(m.messages, Message{
				Role:    RoleError,
				Content: "Memory lookup failed: " + msg.Err.Error(),
			})
		} else {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: msg.Report})
		}
		m.syncViewportContent()
		m.viewport.GotoBottom()

	case CompactDoneMsg:
		m.streaming = false
		if msg.Err != nil {
			m.messages = append(m.messages, Message{
				Role:    RoleError,
				Content: "Compaction failed: " + msg.Err.Error(),
			})
		} else {
			m.messages = append(m.messages, Message{
				Role:    RoleSystem,
				Content: msg.Summary,
			})
		}
		m.syncViewportContent()
		m.viewport.GotoBottom()
		if !m.input.Focused() {
			cmds = append(cmds, m.input.Focus())
		}

	case AgentEventMsg:
		m.handleAgentEvent(msg.Event)
		m.syncViewportContent()
		if m.eventReader != nil {
			return m, m.eventReader.Read()
		}
		return m, nil

	case AgentDoneMsg:
		if m.streaming {
			m.streaming = false
		}
		m.awaitingApproval = false
		m.eventReader = nil
		m.streamCancelled = false
		m.syncViewportContent()
		longReply := m.viewport.TotalLineCount() > m.viewport.VisibleLineCount()
		if longReply && !m.skipScrollAfterStream {
			m.enterScrollMode()
			m.viewport.GotoBottom()
		} else if !m.input.Focused() {
			cmds = append(cmds, m.input.Focus())
		}
		m.skipScrollAfterStream = false
		m.compacting = false

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)

	// Re-highlight the top match when a keystroke changed the typed text.
	// Both guards matter: resetting unconditionally would fire on every
	// spinner tick — which arrives continuously — wiping out the ↑/↓ selection
	// milliseconds after the user made it. Gating on the message type (not
	// just the value) keeps that true even if some other path set the input
	// directly and left lastInputValue stale.
	if _, isKey := msg.(tea.KeyPressMsg); isKey {
		if v := m.input.Value(); v != m.lastInputValue {
			m.lastInputValue = v
			m.suggestIdx = 0
		}
	}

	return m, tea.Batch(cmds...)
}

func (m Model) View() tea.View {
	if !m.ready {
		return tea.NewView("")
	}

	if m.width < 20 || m.height < 10 {
		return tea.NewView(m.styles.WindowTooSmall.Render("Window too small"))
	}

	var b strings.Builder

	header := styles.ApplyBoldForegroundGrad(&m.styles, "Golum Chat", m.styles.Primary, m.styles.Secondary)
	header = lipgloss.NewStyle().Padding(0, 1).Width(m.width).Render(header)
	b.WriteString(header)
	b.WriteString("\n")

	// The suggestion popup has to be sized before the viewport is rendered:
	// it takes its rows out of the viewport's budget rather than adding to the
	// frame. If the frame grows taller than the terminal, the terminal scrolls
	// it and the cursor row computed below no longer matches the screen.
	suggestions := m.slashSuggestions()
	showSuggestions := !m.scrollMode && !m.streaming && len(suggestions) > 0

	popupRows := 0
	if showSuggestions {
		popupRows = len(suggestions) + 1 // rows plus the blank line beneath them
		if budget := m.viewportBase - minViewportRows; popupRows > budget {
			popupRows = budget
		}
		if popupRows <= 1 {
			showSuggestions, popupRows = false, 0
		} else {
			suggestions = suggestions[:popupRows-1]
		}
	}
	// Always derive from the base height rather than subtracting from the
	// current one, so repeated frames can't compound the shrink.
	m.viewport.SetHeight(max(minViewportRows, m.viewportBase-popupRows))

	messagesView := m.renderMessagesView()
	shouldFollowBottom := m.streaming || m.viewport.AtBottom()
	m.viewport.SetContent(messagesView)
	if shouldFollowBottom {
		m.viewport.GotoBottom()
	}
	b.WriteString(m.viewport.View())
	b.WriteString("\n")

	var statusText string
	if showSuggestions {
		statusText = m.styles.Chat.Footer.Render(
			"↑/↓: select | Tab: complete | Enter: run | Esc: cancel")
	} else if m.picker.active {
		statusText = m.styles.Chat.Footer.Render(
			"Sessions · j/k: move | Enter: resume | f: fork | d: delete | Esc: back")
	} else if m.awaitingApproval {
		statusText = m.styles.Chat.Footer.Render("Approve tool? [y]es / [n]o · Esc cancels turn · " + m.approvalSummary)
	} else if m.compacting {
		statusText = m.styles.Chat.Thinking.Render("Compacting context…")
	} else if m.streaming {
		statusText = m.styles.Chat.Thinking.Render("Working… · Esc cancels")
	} else if m.scrollMode {
		statusText = m.styles.Chat.Footer.Render("Scroll long reply · ←/→/↑/↓ or h/j/k/l: move | v: select | y: copy | Ctrl+A: all | i/Esc: type")
		if m.copyStatus != "" {
			statusText = m.styles.Chat.Footer.Render(m.copyStatus + " | v: select | y: copy | Ctrl+A: all | i/Esc: type")
		}
	} else {
		line := "Enter to send · /compact · Esc: scroll/copy · Ctrl+L: new session · Ctrl+C quit"
		if hint := m.sessionFooterHint(); hint != "" {
			line = hint + " · " + line
		}
		statusText = m.styles.Chat.Footer.Render(line)
	}
	statusBar := m.styles.Chat.StatusBar.Width(m.width).Render(statusText)
	b.WriteString(statusBar)
	b.WriteString("\n\n")

	if showSuggestions {
		b.WriteString(m.renderSuggestions(suggestions, m.width-2))
		b.WriteString("\n\n")
	}
	if len(m.queued) > 0 {
		for _, item := range m.queued {
			fmt.Fprintf(&b, "queued steer: %s  (/cancel %s)\n", truncateRunes(item.Content, 72), item.ID)
		}
		b.WriteString("\n")
	}

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
	if m.picker.active {
		return m.renderPicker()
	}
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
		case RoleToolCall, RoleToolResult:
			return lipgloss.NewStyle().Bold(true).Foreground(m.styles.FgMuted)
		case RoleThinking:
			return lipgloss.NewStyle().Bold(true).Foreground(m.styles.FgSubtle)
		default:
			return lipgloss.NewStyle().Bold(true).Foreground(m.styles.FgMuted)
		}
	case selectableLineMeta:
		return lipgloss.NewStyle().Foreground(m.styles.FgSubtle)
	case selectableLineBody:
		switch line.Role {
		case RoleError:
			return lipgloss.NewStyle().Foreground(m.styles.Error)
		case RoleThinking:
			return lipgloss.NewStyle().Foreground(m.styles.FgSubtle)
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
	case RoleToolCall:
		return "Tool"
	case RoleToolResult:
		return "Result"
	case RoleThinking:
		return "Thinking"
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

func (m *Model) showStreamingSpinner() bool {
	if !m.streaming || m.awaitingApproval {
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

func (m *Model) abortStream() {
	m.skipScrollAfterStream = true
	m.awaitingApproval = false
	if m.approvals != nil {
		m.approvals.Decide(false)
	}
	if m.harness != nil {
		m.harness.Abort()
	}
	m.streamCancelled = true
	m.streaming = false
}

func (m *Model) startAgent(text string) tea.Cmd {
	m.streamCancelled = false
	m.skipScrollAfterStream = false
	m.awaitingApproval = false

	if m.harness == nil {
		return func() tea.Msg {
			return AgentEventMsg{Event: harness.AgentEvent{
				Type: harness.EventError,
				Err:  fmt.Errorf("harness not initialized"),
			}}
		}
	}

	applog.Printf("ui: startAgent")
	events, err := m.harness.Prompt(m.ctx, text)
	if err != nil {
		return func() tea.Msg {
			return AgentEventMsg{Event: harness.AgentEvent{Type: harness.EventError, Err: err}}
		}
	}
	m.eventReader = &agentEventReader{events: events}
	return m.eventReader.Read()
}

func (m *Model) handleAgentEvent(ev harness.AgentEvent) {
	if m.streamCancelled && ev.Type != harness.EventTurnDone && ev.Type != harness.EventContentDone {
		return
	}
	switch ev.Type {
	case harness.EventContentDelta:
		if len(m.messages) > 0 && m.messages[len(m.messages)-1].Role == RoleAssistant {
			m.messages[len(m.messages)-1].Content += ev.Content
		} else {
			m.messages = append(m.messages, Message{Role: RoleAssistant, Content: ev.Content})
		}
		m.viewport.GotoBottom()
	case harness.EventQueueConsumed:
		entryID := ev.Meta["entry_id"]
		queued := m.queued[:0]
		for _, item := range m.queued {
			if item.ID != entryID {
				queued = append(queued, item)
			}
		}
		m.queued = queued
	case harness.EventThinkingDelta:
		collapsed := !m.thinkingExpanded
		if len(m.messages) > 0 && m.messages[len(m.messages)-1].Role == RoleThinking {
			m.messages[len(m.messages)-1].Content += ev.Content
		} else {
			m.messages = append(m.messages, Message{Role: RoleThinking, Content: ev.Content, Collapsed: collapsed})
		}
	case harness.EventContentDone:
		if ev.Cancelled {
			m.streaming = false
			m.streamCancelled = false
			m.skipScrollAfterStream = true
			return
		}
		if len(m.messages) > 0 && m.messages[len(m.messages)-1].Role == RoleAssistant {
			m.messages[len(m.messages)-1].Meta = ev.Meta
		}
	case harness.EventToolCallStart:
		name := ""
		if ev.ToolCall != nil {
			name = ev.ToolCall.Name
			if ev.ToolCall.RawArguments != "" {
				name = fmt.Sprintf("%s %s", ev.ToolCall.Name, truncateRunes(ev.ToolCall.RawArguments, 60))
			}
		}
		m.messages = append(m.messages, Message{Role: RoleToolCall, Content: name})
		m.viewport.GotoBottom()
	case harness.EventToolCallAwaitingApproval:
		m.awaitingApproval = true
		if ev.ToolCall != nil {
			m.approvalSummary = fmt.Sprintf("%s %s", ev.ToolCall.Name, truncateRunes(ev.ToolCall.RawArguments, 80))
		}
	case harness.EventToolCallResult:
		summary := ""
		isErr := false
		if ev.ToolResult != nil {
			isErr = ev.ToolResult.IsError
			if ev.ToolResult.Display != "" {
				summary = ev.ToolResult.Display
			} else {
				summary = truncateRunes(ev.ToolResult.Content, 80)
			}
			body := ev.ToolResult.Content
			m.messages = append(m.messages, Message{
				Role:    RoleToolResult,
				Content: summary + "\n" + body,
				IsError: isErr,
			})
		}
		m.viewport.GotoBottom()
	case harness.EventTodosChanged:
		m.todosPanel = tool.FormatTodosForDisplay(ev.Todos)
	case harness.EventCompactionStart:
		m.compacting = true
	case harness.EventCompactionDone:
		m.compacting = false
		if ev.Err != nil {
			m.messages = append(m.messages, Message{
				Role:    RoleError,
				Content: "Compaction failed: " + ev.Err.Error(),
			})
		} else if ev.Compaction != nil && ev.Compaction.Tier != harness.TierNone {
			m.messages = append(m.messages, Message{
				Role: RoleSystem,
				Content: fmt.Sprintf("Context compacted (%s): %d → %d tokens.",
					ev.Compaction.Tier, ev.Compaction.BeforeTokens, ev.Compaction.AfterTokens),
			})
		}
		m.viewport.GotoBottom()
	case harness.EventError:
		applog.Printf("agent error: %v", ev.Err)
		m.streaming = false
		m.awaitingApproval = false
		errMsg := "unknown error"
		if ev.Err != nil {
			errMsg = ev.Err.Error()
		}
		m.messages = append(m.messages, Message{Role: RoleError, Content: "Error: " + errMsg})
		m.viewport.GotoBottom()
	case harness.EventTurnDone:
		m.streaming = false
		m.awaitingApproval = false
		if ev.Cancelled {
			m.skipScrollAfterStream = true
			m.streamCancelled = false
		}
	}
}

// startNewSession swaps in a fresh session, reusing the prompt config captured
// at startup so the skills section survives (rebuilding it from scratch here
// would silently drop project skills from every session after the first).
func (m *Model) startNewSession() {
	todos := tool.NewTodoStore()
	reg := m.harness.Registry()
	reg.Register(tool.NewTodosTool(todos))

	var sess session.Session
	if m.store != nil {
		if created, err := m.store.Create(context.Background()); err == nil {
			sess = created
		} else {
			applog.Printf("ui: new session: %v", err)
		}
	}
	h, err := harness.NewAgentHarness(harness.HarnessConfig{
		Config:    m.cfg,
		Env:       m.harness.Env(),
		Registry:  reg,
		Todos:     todos,
		Session:   sess,
		Approvals: m.approvals,
		// Carry the memory store over: without it the replacement harness has
		// no store, and episodic digests silently stop being recorded for the
		// rest of the run.
		Memory:    m.memory,
		PromptCfg: m.promptCfg,
		Skills:    m.harness.Skills(),
	})
	if err != nil {
		applog.Printf("ui: new harness: %v", err)
		m.harness.NewSession(m.cfg, m.promptCfg)
		return
	}
	m.harness = h
	// Repoint the shared handle the memory tool reads, or facts stored after
	// this point would be attributed to the previous session.
	if m.active != nil {
		m.active.set(sess)
	}
	m.messages = nil
	m.todosPanel = ""
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func (m Model) sessionFooterHint() string {
	if m.harness == nil || m.harness.Session() == nil {
		return ""
	}
	cm := m.harness.Session().ContextManager()
	if cm == nil {
		return ""
	}
	tu := cm.TotalUsage()
	var b strings.Builder
	if tu.TotalTokens > 0 {
		fmt.Fprintf(&b, "session Σ %d tok", tu.TotalTokens)
	}
	if ratio := cm.ContextUsageRatio(); ratio > 0 {
		if b.Len() > 0 {
			b.WriteString(" · ")
		}
		fmt.Fprintf(&b, "ctx %.0f%%", ratio*100)
		if cm.ShouldCompact() {
			b.WriteString(" (compacting soon)")
		}
	}
	if m.todosPanel != "" {
		if b.Len() > 0 {
			b.WriteString(" · ")
		}
		b.WriteString("todos updated")
	}
	return b.String()
}
