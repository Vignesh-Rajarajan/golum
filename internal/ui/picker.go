package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
)

// SessionsLoadedMsg carries the result of listing saved sessions.
type SessionsLoadedMsg struct {
	Sessions []session.SessionMeta
	Err      error
}

// SessionOpenedMsg carries the result of resuming or forking a session.
type SessionOpenedMsg struct {
	Session session.Session
	Err     error
}

// pickerState holds the session browser's UI state.
type pickerState struct {
	active   bool
	loading  bool
	cursor   int
	sessions []session.SessionMeta
	err      error
	status   string
}

// openPicker loads the session list and shows the browser.
func (m *Model) openPicker() tea.Cmd {
	if m.store == nil {
		m.picker.err = fmt.Errorf("session storage unavailable")
		m.picker.active = true
		return nil
	}
	m.picker.active = true
	m.picker.loading = true
	m.picker.err = nil
	m.picker.status = ""
	m.input.Blur()

	store := m.store
	current := m.currentSessionID()
	return func() tea.Msg {
		list, err := store.List(context.Background())
		// Drop the session we are sitting in — resuming it is a no-op and
		// deleting it would pull the rug out from under the running turn.
		filtered := list[:0]
		for _, s := range list {
			if s.ID != current {
				filtered = append(filtered, s)
			}
		}
		return SessionsLoadedMsg{Sessions: filtered, Err: err}
	}
}

func (m *Model) currentSessionID() string {
	if m.harness == nil || m.harness.Session() == nil {
		return ""
	}
	return m.harness.Session().ID()
}

func (m *Model) closePicker() tea.Cmd {
	m.picker = pickerState{}
	return m.input.Focus()
}

// handlePickerKey routes keys while the browser is open. Returns handled=false
// when the picker is not active.
func (m *Model) handlePickerKey(key tea.Key) (tea.Cmd, bool) {
	if !m.picker.active {
		return nil, false
	}
	switch key.String() {
	case "esc", "q":
		return m.closePicker(), true
	case "j", "down":
		if m.picker.cursor < len(m.picker.sessions)-1 {
			m.picker.cursor++
		}
		return nil, true
	case "k", "up":
		if m.picker.cursor > 0 {
			m.picker.cursor--
		}
		return nil, true
	case "enter", "r":
		return m.resumeSelected(false), true
	case "f":
		return m.resumeSelected(true), true
	case "d":
		return m.deleteSelected(), true
	default:
		return nil, true // swallow everything else while browsing
	}
}

func (m *Model) selected() (session.SessionMeta, bool) {
	if m.picker.cursor < 0 || m.picker.cursor >= len(m.picker.sessions) {
		return session.SessionMeta{}, false
	}
	return m.picker.sessions[m.picker.cursor], true
}

// resumeSelected opens (or forks) the highlighted session off the UI goroutine.
func (m *Model) resumeSelected(fork bool) tea.Cmd {
	meta, ok := m.selected()
	if !ok || m.store == nil {
		return nil
	}
	store := m.store
	id := meta.ID
	m.picker.status = "Opening…"
	return func() tea.Msg {
		ctx := context.Background()
		var (
			sess session.Session
			err  error
		)
		if fork {
			sess, err = store.Fork(ctx, id, "")
		} else {
			sess, err = store.Open(ctx, id)
		}
		return SessionOpenedMsg{Session: sess, Err: err}
	}
}

func (m *Model) deleteSelected() tea.Cmd {
	meta, ok := m.selected()
	if !ok || m.store == nil {
		return nil
	}
	if err := m.store.Delete(context.Background(), meta.ID); err != nil {
		m.picker.err = err
		return nil
	}
	m.picker.sessions = append(m.picker.sessions[:m.picker.cursor], m.picker.sessions[m.picker.cursor+1:]...)
	if m.picker.cursor >= len(m.picker.sessions) {
		m.picker.cursor = max(0, len(m.picker.sessions)-1)
	}
	m.picker.status = "Deleted " + meta.ID
	return nil
}

// adoptSession swaps the harness onto an already-opened session.
func (m *Model) adoptSession(sess session.Session) {
	if sess == nil || m.harness == nil {
		return
	}
	if err := m.harness.SetSession(sess); err != nil {
		m.picker.err = err
		return
	}
	if m.active != nil {
		m.active.set(sess)
	}
	m.messages = transcriptFromSession(sess)
	m.todosPanel = ""
}

func (m *Model) renderPicker() string {
	var b strings.Builder
	title := m.styles.Chat.Footer.Render("Sessions")
	b.WriteString(title)
	b.WriteString("\n\n")

	switch {
	case m.picker.loading:
		b.WriteString(m.styles.Subtle.Render("Loading…"))
	case m.picker.err != nil:
		b.WriteString(m.styles.Chat.ErrorMessage.Render("Error: " + m.picker.err.Error()))
	case len(m.picker.sessions) == 0:
		b.WriteString(m.styles.Subtle.Render("No other saved sessions."))
	default:
		for i, s := range m.picker.sessions {
			line := fmt.Sprintf("%-26s  %-10s  %3d entries  %s",
				s.ID, relTime(s.UpdatedAt), s.EntryCount, s.Label)
			if i == m.picker.cursor {
				b.WriteString(lipgloss.NewStyle().
					Foreground(m.styles.BgBase).
					Background(m.styles.Primary).
					Render("› " + line))
			} else {
				b.WriteString(lipgloss.NewStyle().
					Foreground(m.styles.FgBase).
					Render("  " + line))
			}
			b.WriteString("\n")
		}
	}
	if m.picker.status != "" {
		b.WriteString("\n")
		b.WriteString(m.styles.Subtle.Render(m.picker.status))
	}
	return b.String()
}

func relTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Local().Format("01-02 15:04")
	}
}
