package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness"
)

func TestSlashQuery_ActiveOnlyBeforeFirstSpace(t *testing.T) {
	cases := []struct {
		value      string
		wantQuery  string
		wantActive bool
	}{
		{"/", "", true},
		{"/comp", "comp", true},
		{"/compact", "compact", true},
		{"/compact ", "", false},
		{"/memory some query", "", false},
		{"hello", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		q, active := slashQuery(tc.value)
		if q != tc.wantQuery || active != tc.wantActive {
			t.Errorf("slashQuery(%q) = (%q, %v), want (%q, %v)",
				tc.value, q, active, tc.wantQuery, tc.wantActive)
		}
	}
}

func TestMatchingSlashCommands_EmptyQueryShowsAll(t *testing.T) {
	got := matchingSlashCommands("")
	if len(got) != len(slashCommandCatalog) {
		t.Fatalf("expected the full catalog, got %d of %d", len(got), len(slashCommandCatalog))
	}
}

func TestMatchingSlashCommands_PrefixFilterIsCaseInsensitive(t *testing.T) {
	got := matchingSlashCommands("COMP")
	if len(got) != 1 || got[0].Name != "compact" {
		t.Fatalf("expected only compact, got %+v", got)
	}
}

func TestMatchingSlashCommands_NoMatches(t *testing.T) {
	if got := matchingSlashCommands("zzz"); len(got) != 0 {
		t.Fatalf("expected no matches, got %+v", got)
	}
}

// Every catalog entry must actually be dispatchable, or the popup would offer
// a command that reports "Unknown command" when run.
func TestSlashCommandCatalog_EveryEntryDispatches(t *testing.T) {
	for _, c := range slashCommandCatalog {
		m := newTestModel(nil, false)
		m.handleSlashCommand("/" + c.Name)
		if len(m.messages) == 0 {
			continue // command scheduled work instead of appending a message; fine
		}
		if m.messages[0].Role == RoleError && strings.Contains(m.messages[0].Content, "Unknown command") {
			t.Errorf("catalog entry %q does not dispatch: %s", c.Name, m.messages[0].Content)
		}
	}
}

func TestSlashHelp_MentionsEveryCatalogEntry(t *testing.T) {
	help := slashHelp()
	for _, c := range slashCommandCatalog {
		if !strings.Contains(help, "/"+c.Name) {
			t.Errorf("help text missing /%s:\n%s", c.Name, help)
		}
	}
}

// ---- key handling ------------------------------------------------------

func TestUpdate_TypingSlashShowsSuggestions(t *testing.T) {
	m := newTestModel(nil, false)
	m.width, m.height, m.ready = 80, 24, true
	m.input.SetValue("/")

	if got := m.slashSuggestions(); len(got) != len(slashCommandCatalog) {
		t.Fatalf("expected all commands suggested for bare '/', got %d", len(got))
	}
}

func TestUpdate_ArrowKeysNavigateSuggestions(t *testing.T) {
	m := newTestModel(nil, false)
	m.width, m.height, m.ready = 80, 24, true
	m.input.SetValue("/")

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got := asModel(t, updated)
	if got.suggestIdx != 1 {
		t.Fatalf("expected suggestIdx=1 after one Down, got %d", got.suggestIdx)
	}

	updated2, _ := got.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	got2 := asModel(t, updated2)
	if got2.suggestIdx != 0 {
		t.Fatalf("expected suggestIdx=0 after Up, got %d", got2.suggestIdx)
	}
}

func TestUpdate_ArrowKeysWrapAround(t *testing.T) {
	m := newTestModel(nil, false)
	m.width, m.height, m.ready = 80, 24, true
	m.input.SetValue("/")

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	got := asModel(t, updated)
	if got.suggestIdx != len(slashCommandCatalog)-1 {
		t.Fatalf("Up from the top should wrap to the last entry, got %d", got.suggestIdx)
	}
}

func TestUpdate_TabCompletesWithoutSubmitting(t *testing.T) {
	m := newTestModel(nil, false)
	m.width, m.height, m.ready = 80, 24, true
	m.input.SetValue("/comp")

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	got := asModel(t, updated)
	if got.input.Value() != "/compact " {
		t.Fatalf("expected Tab to complete to '/compact ', got %q", got.input.Value())
	}
	if len(got.messages) != 0 {
		t.Fatal("Tab must not submit anything")
	}
}

func TestUpdate_EscClearsInputWithoutEnteringScrollMode(t *testing.T) {
	m := newTestModel(nil, false)
	m.width, m.height, m.ready = 80, 24, true
	m.input.SetValue("/comp")

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := asModel(t, updated)
	if got.input.Value() != "" {
		t.Fatalf("expected Esc to clear the input, got %q", got.input.Value())
	}
	if got.scrollMode {
		t.Fatal("Esc while suggestions are open must not enter scroll mode")
	}
}

func TestUpdate_EnterRunsHighlightedSuggestionEvenWhenPartial(t *testing.T) {
	m := newTestModel(nil, false)
	m.width, m.height, m.ready = 80, 24, true
	m.input.SetValue("/comp") // partial — would be "unknown command" if sent verbatim

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := asModel(t, updated)
	if got.input.Value() != "" {
		t.Fatalf("expected input cleared after Enter, got %q", got.input.Value())
	}
	if cmd == nil {
		t.Fatal("expected Enter to schedule sending the completed command")
	}
	msg := cmd()
	submit, ok := msg.(InputSubmitMsg)
	if !ok || submit.Text != "/compact" {
		t.Fatalf("expected InputSubmitMsg{/compact}, got %#v", msg)
	}
}

func TestUpdate_SuggestionsDoNotInterceptWhileStreaming(t *testing.T) {
	m := newTestModel(nil, true) // streaming = true
	m.width, m.height, m.ready = 80, 24, true
	m.input.SetValue("/comp")

	// Down should fall through to the plain textinput, not navigate a hidden popup.
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got := asModel(t, updated)
	if got.suggestIdx != 0 {
		t.Fatalf("suggestions must be inert while streaming, suggestIdx=%d", got.suggestIdx)
	}
}

func TestUpdate_SuggestionsDoNotInterceptInScrollMode(t *testing.T) {
	m := newTestModel(nil, false)
	m.width, m.height, m.ready = 80, 24, true
	m.scrollMode = true
	m.input.SetValue("/comp")

	// Esc in scroll mode must keep doing its normal job (exit scroll mode),
	// not the suggestion popup's "clear input" behavior.
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := asModel(t, updated)
	if got.input.Value() != "/comp" {
		t.Fatalf("scroll-mode Esc must not touch the input, got %q", got.input.Value())
	}
	if got.scrollMode {
		t.Fatal("Esc in scroll mode should exit scroll mode")
	}
}

func TestUpdate_TypingResetsSuggestionIndex(t *testing.T) {
	m := newTestModel(nil, false)
	m.width, m.height, m.ready = 80, 24, true
	m.input.SetValue("/")

	moved, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got := asModel(t, moved)
	if got.suggestIdx == 0 {
		t.Fatal("setup: expected the down-press to move the selection")
	}

	typed, _ := got.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	gotTyped := asModel(t, typed)
	if gotTyped.suggestIdx != 0 {
		t.Fatalf("typing should reset the highlight to the top match, got %d", gotTyped.suggestIdx)
	}
}

func TestSelectedSuggestion_ClampsToShrunkList(t *testing.T) {
	m := newTestModel(nil, false)
	m.suggestIdx = 5 // stale, from a longer list
	got := m.selectedSuggestion(matchingSlashCommands("compact"))
	if got.Name != "compact" {
		t.Fatalf("expected the single match despite a stale index, got %+v", got)
	}
}

// ---- rendering / cursor-position invariant ------------------------------

// This is the regression test for the bug where the real text-input cursor
// rendered inside the popup instead of on the input line below it: an
// unselected suggestion row had no width cap, so a row wider than the
// terminal got wrapped by the terminal itself (no "\n" to show for it),
// undercounting inputY in View().
func TestRenderSuggestions_ExactlyOneTerminalLinePerRow(t *testing.T) {
	m := newTestModel(nil, false)

	for _, width := range []int{100, 40, 20, 10, 5} {
		out := m.renderSuggestions(slashCommandCatalog, width)
		gotLines := strings.Count(out, "\n") + 1
		if gotLines != len(slashCommandCatalog) {
			t.Errorf("width=%d: got %d rendered lines, want %d (one per suggestion) — "+
				"a row must have wrapped, which desyncs the input cursor position",
				width, gotLines, len(slashCommandCatalog))
		}
	}
}

// Every rendered row's *visible* width (ANSI codes stripped) must never
// exceed the requested width, on both the highlighted and plain rows —
// otherwise the terminal wraps it and the "one line per row" invariant above
// doesn't actually hold on screen even though countLines still divides the
// string one newline per suggestion.
func TestRenderSuggestions_NeverExceedsRequestedWidth(t *testing.T) {
	m := newTestModel(nil, false)

	for _, width := range []int{60, 30, 15, 8} {
		out := m.renderSuggestions(slashCommandCatalog, width)
		for i, line := range strings.Split(out, "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("width=%d row=%d: visible width %d exceeds %d\nline=%q",
					width, i, w, width, line)
			}
		}
	}
}

func TestRenderSuggestions_EmptyWhenNoSuggestions(t *testing.T) {
	m := newTestModel(nil, false)
	if out := m.renderSuggestions(nil, 80); out != "" {
		t.Fatalf("expected empty output for no suggestions, got %q", out)
	}
}

func TestFitToWidth(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is too long", 10, "this is t…"},
		{"x", 1, "x"},
		{"toolong", 1, "t"},
		{"anything", 0, ""},
	}
	for _, tc := range cases {
		got := fitToWidth(tc.in, tc.width)
		if got != tc.want {
			t.Errorf("fitToWidth(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
		}
		if r := []rune(got); tc.width >= 0 && len(r) > tc.width {
			t.Errorf("fitToWidth(%q, %d) = %q is longer than width", tc.in, tc.width, got)
		}
	}
}

// View() must place the real text-input cursor below the popup, never on one
// of its rows — this is what the screenshot bug looked like from the user's
// side (the cursor appeared to sit inside "/memory").
func TestView_CursorLandsBelowSuggestionPopup(t *testing.T) {
	m := newTestModel(nil, false)
	m.width, m.height, m.ready = 80, 24, true
	// NewModel() disables the virtual cursor so View() reports a real
	// terminal cursor via v.Cursor (which is what this test checks the
	// position of); textinput.New()'s own default is the virtual cursor.
	m.input.SetVirtualCursor(false)
	m.input.SetValue("/m")
	m.input.Focus()

	v := m.View()
	if v.Cursor == nil {
		t.Fatal("expected a cursor to be set while the input is focused")
	}

	rows := strings.Split(v.Content, "\n")
	if v.Cursor.Y < 0 || v.Cursor.Y >= len(rows) {
		t.Fatalf("cursor row %d out of range (0..%d)", v.Cursor.Y, len(rows)-1)
	}
	cursorRow := rows[v.Cursor.Y]
	// A popup row starts (after styling) with a slash-command name; the real
	// input line does not contain any of the catalog's command names.
	if strings.Contains(cursorRow, "/memory") || strings.Contains(cursorRow, "/compact") {
		t.Fatalf("cursor landed inside the suggestion popup, row=%q", cursorRow)
	}
}

// ---- selection stability ------------------------------------------------

// Regression: the spinner re-arms itself continuously, so spinner.TickMsg
// flows through Update() constantly. An unconditional `suggestIdx = 0` at the
// bottom of Update() wiped the user's ↑/↓ selection milliseconds after they
// made it — the selection appeared to "snap back to the top on its own".
func TestUpdate_SpinnerTickDoesNotResetSelection(t *testing.T) {
	m := newTestModel(nil, false)
	m.width, m.height, m.ready = 80, 24, true
	m.input.SetValue("/")

	moved, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got := asModel(t, moved)
	if got.suggestIdx != 1 {
		t.Fatalf("setup: expected suggestIdx=1, got %d", got.suggestIdx)
	}

	for i := 0; i < 5; i++ {
		ticked, _ := got.Update(spinner.TickMsg{})
		got = asModel(t, ticked)
	}
	if got.suggestIdx != 1 {
		t.Fatalf("spinner ticks reset the selection to %d; it must stay at 1", got.suggestIdx)
	}
}

// Any message that isn't a keystroke must likewise leave the selection alone.
func TestUpdate_UnrelatedMessagesDoNotResetSelection(t *testing.T) {
	m := newTestModel(nil, false)
	m.width, m.height, m.ready = 80, 24, true
	m.input.SetValue("/")

	moved, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got := asModel(t, moved)

	for _, msg := range []tea.Msg{
		spinner.TickMsg{},
		AgentEventMsg{Event: harness.AgentEvent{Type: harness.EventContentDelta, Content: "x"}},
		MemoryReportMsg{Report: "something"},
	} {
		updated, _ := got.Update(msg)
		got = asModel(t, updated)
	}
	if got.suggestIdx != 1 {
		t.Fatalf("unrelated messages reset the selection to %d; expected 1", got.suggestIdx)
	}
}

// ---- layout / frame height ---------------------------------------------

// Regression for the misplaced cursor: with the popup open the frame used to
// be 3 rows taller than the terminal (header + viewport + status + blank +
// popup + blank + input), so the terminal scrolled it and the computed cursor
// row no longer matched the screen.
func TestView_FrameNeverExceedsTerminalHeight(t *testing.T) {
	for _, height := range []int{24, 20, 15, 12, 10} {
		m := newTestModel(nil, false)
		m.input.SetVirtualCursor(false)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: height})
		m = asModel(t, updated)
		m.input.SetValue("/") // opens the popup with every command listed

		rows := strings.Count(m.View().Content, "\n") + 1
		if rows > height {
			t.Errorf("height=%d: frame rendered %d rows, exceeding the terminal "+
				"— the terminal will scroll and misplace the cursor", height, rows)
		}
	}
}

// The popup must not squeeze the transcript out of existence on a short
// terminal; it gives up rows instead.
func TestView_ShortTerminalKeepsViewportUsable(t *testing.T) {
	m := newTestModel(nil, false)
	m.input.SetVirtualCursor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = asModel(t, updated)
	m.input.SetValue("/")

	if m.viewport.Height() < minViewportRows {
		t.Fatalf("viewport shrank to %d rows, below the %d minimum",
			m.viewport.Height(), minViewportRows)
	}
}
