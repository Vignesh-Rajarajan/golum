package styles

import (
	"image/color"

	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
)

const (
	CheckIcon   string = "✓"
	SpinnerIcon string = "⋯"
	LoadingIcon string = "⟳"
	ModelIcon   string = "◇"

	ArrowRightIcon string = "→"

	ToolPending string = "●"
	ToolSuccess string = "✓"
	ToolError   string = "×"

	BorderThin  string = "│"
	BorderThick string = "▌"

	UserIcon      string = "👤"
	AssistantIcon string = "🤖"
)

type Styles struct {
	Base      lipgloss.Style
	Muted     lipgloss.Style
	HalfMuted lipgloss.Style
	Subtle    lipgloss.Style

	Primary   color.Color
	Secondary color.Color
	Tertiary  color.Color

	BgBase        color.Color
	BgBaseLighter color.Color
	BgSubtle      color.Color
	BgOverlay     color.Color

	FgBase      color.Color
	FgMuted     color.Color
	FgHalfMuted color.Color
	FgSubtle    color.Color

	Border      color.Color
	BorderColor color.Color

	Error   color.Color
	Warning color.Color
	Info    color.Color
	Success color.Color

	White     color.Color
	Blue      color.Color
	BlueDark  color.Color
	Green     color.Color
	GreenDark color.Color
	Red       color.Color
	RedDark   color.Color
	Yellow    color.Color

	Markdown ansi.StyleConfig

	Chat struct {
		UserMessage      lipgloss.Style
		AssistantMessage lipgloss.Style
		SystemMessage    lipgloss.Style
		ErrorMessage     lipgloss.Style
		InputBox         lipgloss.Style
		InputPrompt      lipgloss.Style
		Header           lipgloss.Style
		Footer           lipgloss.Style
		StatusBar        lipgloss.Style
		Thinking         lipgloss.Style
		Spinner          lipgloss.Style
	}

	BorderFocus lipgloss.Style
	BorderBlur  lipgloss.Style

	WindowTooSmall lipgloss.Style
}

func DefaultStyles() Styles {
	primary := lipgloss.Color("#8B5CF6")
	secondary := lipgloss.Color("#FF9F1C")
	tertiary := lipgloss.Color("#35D48A")

	bgBase := lipgloss.Color("#12121C")
	bgBaseLighter := lipgloss.Color("#1B1C2A")
	bgSubtle := lipgloss.Color("#262A3D")
	bgOverlay := lipgloss.Color("#3D4160")

	fgBase := lipgloss.Color("#E4E7F7")
	fgMuted := lipgloss.Color("#B8BFE0")
	fgHalfMuted := lipgloss.Color("#8890B8")
	fgSubtle := lipgloss.Color("#656B92")

	border := lipgloss.Color("#454B72")
	borderFocus := lipgloss.Color("#8B5CF6")

	error := lipgloss.Color("#FF5568")
	warning := lipgloss.Color("#FFD166")
	info := lipgloss.Color("#4EA8FF")
	success := lipgloss.Color("#35D48A")

	white := lipgloss.Color("#FFFFFF")
	blue := lipgloss.Color("#4EA8FF")
	blueDark := lipgloss.Color("#2360C7")
	green := lipgloss.Color("#35D48A")
	greenDark := lipgloss.Color("#0E9F6E")
	red := lipgloss.Color("#FF5568")
	redDark := lipgloss.Color("#C22B45")
	yellow := lipgloss.Color("#FFD166")

	base := lipgloss.NewStyle().Foreground(fgBase)

	s := Styles{}

	s.Primary = primary
	s.Secondary = secondary
	s.Tertiary = tertiary

	s.BgBase = bgBase
	s.BgBaseLighter = bgBaseLighter
	s.BgSubtle = bgSubtle
	s.BgOverlay = bgOverlay

	s.FgBase = fgBase
	s.FgMuted = fgMuted
	s.FgHalfMuted = fgHalfMuted
	s.FgSubtle = fgSubtle

	s.Border = border
	s.BorderColor = borderFocus

	s.Error = error
	s.Warning = warning
	s.Info = info
	s.Success = success

	s.White = white
	s.Blue = blue
	s.BlueDark = blueDark
	s.Green = green
	s.GreenDark = greenDark
	s.Red = red
	s.RedDark = redDark
	s.Yellow = yellow

	s.Base = base
	s.Muted = base.Foreground(fgMuted)
	s.HalfMuted = base.Foreground(fgHalfMuted)
	s.Subtle = base.Foreground(fgSubtle)

	s.Markdown = ansi.StyleConfig{
		Document: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: stringPtr("#E4E7F7"),
			},
		},
		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{},
			Indent:         uintPtr(1),
			IndentToken:    stringPtr("│ "),
		},
		List: ansi.StyleList{
			LevelIndent: 2,
		},
		Heading: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				BlockSuffix: "\n",
				Color:       stringPtr("#4EA8FF"),
				Bold:        boolPtr(true),
			},
		},
		H1: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix:          " ",
				Suffix:          " ",
				Color:           stringPtr("#FFFFFF"),
				BackgroundColor: stringPtr("#8B5CF6"),
				Bold:            boolPtr(true),
			},
		},
		H2: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: "## ",
			},
		},
		H3: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: "### ",
			},
		},
		Strikethrough: ansi.StylePrimitive{
			CrossedOut: boolPtr(true),
		},
		Emph: ansi.StylePrimitive{
			Italic: boolPtr(true),
		},
		Strong: ansi.StylePrimitive{
			Bold: boolPtr(true),
		},
		Item: ansi.StylePrimitive{
			BlockPrefix: "• ",
		},
		Enumeration: ansi.StylePrimitive{
			BlockPrefix: ". ",
		},
		Task: ansi.StyleTask{
			Ticked:   "[✓] ",
			Unticked: "[ ] ",
		},
		Link: ansi.StylePrimitive{
			Color:     stringPtr("#4EA8FF"),
			Underline: boolPtr(true),
		},
		LinkText: ansi.StylePrimitive{
			Color: stringPtr("#35D48A"),
			Bold:  boolPtr(true),
		},
		Code: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix:          " ",
				Suffix:          " ",
				Color:           stringPtr("#FF5568"),
				BackgroundColor: stringPtr("#3D4160"),
			},
		},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{
					Color: stringPtr("#E4E7F7"),
				},
				Margin: uintPtr(2),
			},
			Chroma: &ansi.Chroma{
				Text: ansi.StylePrimitive{
					Color: stringPtr("#E4E7F7"),
				},
				Comment: ansi.StylePrimitive{
					Color: stringPtr("#656B92"),
				},
				Keyword: ansi.StylePrimitive{
					Color: stringPtr("#8B5CF6"),
				},
				KeywordType: ansi.StylePrimitive{
					Color: stringPtr("#35D48A"),
				},
				Operator: ansi.StylePrimitive{
					Color: stringPtr("#FF5568"),
				},
				Name: ansi.StylePrimitive{
					Color: stringPtr("#E4E7F7"),
				},
				NameFunction: ansi.StylePrimitive{
					Color: stringPtr("#4EA8FF"),
				},
				LiteralString: ansi.StylePrimitive{
					Color: stringPtr("#FFD166"),
				},
				LiteralNumber: ansi.StylePrimitive{
					Color: stringPtr("#FFA657"),
				},
				Background: ansi.StylePrimitive{
					BackgroundColor: stringPtr("#12121C"),
				},
			},
		},
	}

	messageBorder := lipgloss.Border{
		Left: BorderThick,
	}

	s.Chat.UserMessage = lipgloss.NewStyle().
		PaddingLeft(1).
		BorderLeft(true).
		BorderStyle(messageBorder).
		BorderForeground(primary)

	s.Chat.AssistantMessage = lipgloss.NewStyle().
		PaddingLeft(1).
		BorderLeft(true).
		BorderStyle(messageBorder).
		BorderForeground(greenDark)

	s.Chat.SystemMessage = lipgloss.NewStyle().
		PaddingLeft(1).
		Foreground(fgHalfMuted)

	s.Chat.ErrorMessage = lipgloss.NewStyle().
		Padding(0, 1).
		Foreground(white).
		Background(redDark)

	s.Chat.InputBox = lipgloss.NewStyle().
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border)

	s.Chat.InputPrompt = lipgloss.NewStyle().
		Foreground(greenDark).
		SetString("> ")

	s.Chat.Header = lipgloss.NewStyle().
		Bold(true).
		Foreground(primary).
		Padding(0, 1)

	s.Chat.Footer = lipgloss.NewStyle().
		Foreground(fgSubtle).
		Padding(0, 1)

	s.Chat.StatusBar = lipgloss.NewStyle().
		Background(bgSubtle).
		Foreground(fgMuted).
		Padding(0, 1)

	s.Chat.Thinking = lipgloss.NewStyle().
		Foreground(info).
		Italic(true)

	s.Chat.Spinner = lipgloss.NewStyle().
		Foreground(primary)

	s.BorderFocus = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderFocus).
		Padding(0, 1)

	s.BorderBlur = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 1)

	s.WindowTooSmall = s.Muted

	return s
}

func boolPtr(b bool) *bool       { return &b }
func stringPtr(s string) *string { return &s }
func uintPtr(u uint) *uint       { return &u }
