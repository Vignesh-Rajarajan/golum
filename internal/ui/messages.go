package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
)

type StreamMsg struct {
	Event llm.StreamEvent
}

type StreamDoneMsg struct{}

type ErrorMsg struct {
	Error error
}

type InputSubmitMsg struct {
	Text string
}

type streamReader struct {
	events <-chan llm.StreamEvent
}

func (r *streamReader) Read() tea.Cmd {
	return func() tea.Msg {
		event, ok := <-r.events
		if !ok {
			return StreamDoneMsg{}
		}
		return StreamMsg{Event: event}
	}
}
