package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
)

type AgentEventMsg struct {
	Event harness.AgentEvent
}

type AgentDoneMsg struct{}

type ErrorMsg struct {
	Error error
}

type InputSubmitMsg struct {
	Text string
}

type SteerQueuedMsg struct {
	Entry session.ProvisionedEntry
	Err   error
}

type ResumeRunDoneMsg struct{ Err error }

type agentEventReader struct {
	events <-chan harness.AgentEvent
}

func (r *agentEventReader) Read() tea.Cmd {
	return func() tea.Msg {
		event, ok := <-r.events
		if !ok {
			return AgentDoneMsg{}
		}
		return AgentEventMsg{Event: event}
	}
}
