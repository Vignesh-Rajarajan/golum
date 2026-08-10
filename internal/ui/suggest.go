package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// minViewportRows is the smallest transcript we will shrink to when making
// room for the suggestion popup.
const minViewportRows = 5

// slashCommandSpec describes one local command for both /help and the live
// suggestion popup, so the two can never drift apart.
type slashCommandSpec struct {
	Name string // without the leading slash
	Args string // hint shown after the name, e.g. "[query]"; empty if none
	Desc string
}

// slashCommandCatalog is the single source of truth for discoverable
// commands. "/resume" also works (handleSlashCommand treats it as an alias
// for "sessions") but is intentionally left out here — it is a shortcut, not
// a separate command to advertise.
var slashCommandCatalog = []slashCommandSpec{
	{Name: "clear", Desc: "Clear the screen and start a fresh conversation"},
	{Name: "compact", Desc: "Summarize older turns to reclaim context now"},
	{Name: "context", Desc: "Show context usage"},
	{Name: "sessions", Desc: "Browse, resume, fork or delete saved sessions"},
	{Name: "reindex", Desc: "Rebuild the project map used by semantic memory"},
	{Name: "memory", Args: "[query|tier]", Desc: "List what's remembered, or search it"},
	{Name: "help", Desc: "Show this list"},
}

func slashHelp() string {
	var b strings.Builder
	b.WriteString("Commands:\n")
	width := 0
	for _, c := range slashCommandCatalog {
		if n := len("/" + c.Name + " " + c.Args); n > width {
			width = n
		}
	}
	for _, c := range slashCommandCatalog {
		label := "/" + c.Name
		if c.Args != "" {
			label += " " + c.Args
		}
		b.WriteString("  ")
		b.WriteString(label)
		b.WriteString(strings.Repeat(" ", width-len(label)+2))
		b.WriteString("— ")
		b.WriteString(c.Desc)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// slashQuery reports the text typed after "/" and whether the input is still
// in "typing a command name" mode. Once a space appears the user has moved on
// to arguments (or plain text starting with a literal "/"), so the popup
// closes rather than fighting with normal typing.
func slashQuery(value string) (query string, active bool) {
	if !strings.HasPrefix(value, "/") {
		return "", false
	}
	body := value[1:]
	if strings.ContainsAny(body, " \t") {
		return "", false
	}
	return body, true
}

// matchingSlashCommands returns catalog entries whose name has the given
// prefix (case-insensitive). An empty query matches everything, so typing a
// bare "/" shows the full list.
func matchingSlashCommands(query string) []slashCommandSpec {
	if query == "" {
		return slashCommandCatalog
	}
	q := strings.ToLower(query)
	var out []slashCommandSpec
	for _, c := range slashCommandCatalog {
		if strings.HasPrefix(strings.ToLower(c.Name), q) {
			out = append(out, c)
		}
	}
	return out
}

// slashSuggestions returns the commands matching what's currently typed, or
// nil when the popup should not be showing.
func (m Model) slashSuggestions() []slashCommandSpec {
	query, active := slashQuery(m.input.Value())
	if !active {
		return nil
	}
	return matchingSlashCommands(query)
}

// selectedSuggestion returns the highlighted entry, clamped to the current
// (possibly just-shrunk) list.
func (m Model) selectedSuggestion(suggestions []slashCommandSpec) slashCommandSpec {
	i := clampInt(m.suggestIdx, 0, len(suggestions)-1)
	return suggestions[i]
}

// renderSuggestions draws the popup shown above the input while a slash
// command is being typed.
//
// Every row MUST render as exactly one terminal line. View() places the real
// text-input cursor by counting the "\n" bytes written before it
// (countLines) and offsetting past them — a row whose plain-text width
// exceeds the terminal width would get wrapped by the terminal itself with
// no "\n" to account for it, undercounting inputY and landing the cursor
// inside the popup instead of on the input line below it. fitToWidth is what
// guarantees that never happens.
func (m Model) renderSuggestions(suggestions []slashCommandSpec, width int) string {
	if len(suggestions) == 0 || width <= 1 {
		return ""
	}
	selected := clampInt(m.suggestIdx, 0, len(suggestions)-1)

	nameWidth := 0
	for _, c := range suggestions {
		if n := len(c.Name) + len(c.Args) + 1; n > nameWidth {
			nameWidth = n
		}
	}

	var b strings.Builder
	for i, c := range suggestions {
		label := "/" + c.Name
		if c.Args != "" {
			label += " " + c.Args
		}
		padded := label + strings.Repeat(" ", max(0, nameWidth+2-len(label)))
		plain := fitToWidth(" "+padded+c.Desc, width)

		if i == selected {
			b.WriteString(lipgloss.NewStyle().
				Foreground(m.styles.BgBase).
				Background(m.styles.Primary).
				Width(width).
				Render(plain))
		} else {
			b.WriteString(lipgloss.NewStyle().
				Foreground(m.styles.FgMuted).
				Render(plain))
		}
		if i < len(suggestions)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// fitToWidth truncates s (measured in runes) to at most width, adding an
// ellipsis when truncated. It never returns something wider than width.
func fitToWidth(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		if width <= 0 {
			return ""
		}
		return string(r[:width])
	}
	return string(r[:width-1]) + "…"
}
