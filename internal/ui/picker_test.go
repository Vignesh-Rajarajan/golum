package ui

import (
	"fmt"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
)

func pickerModel(n int) Model {
	m := newTestModel(nil, false)
	m.picker.active = true
	for i := 0; i < n; i++ {
		m.picker.sessions = append(m.picker.sessions, session.SessionMeta{
			ID:        fmt.Sprintf("sess_%d", i),
			UpdatedAt: time.Now(),
		})
	}
	return m
}

func keyPress(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: rune(s[0])}
}

func TestPicker_CursorMovementClamps(t *testing.T) {
	m := pickerModel(3)

	// Up at the top stays put.
	if _, handled := m.handlePickerKey(keyPress("k").Key()); !handled {
		t.Fatal("picker should handle k")
	}
	if m.picker.cursor != 0 {
		t.Fatalf("cursor went above the first row: %d", m.picker.cursor)
	}

	for i := 0; i < 5; i++ {
		m.handlePickerKey(keyPress("j").Key())
	}
	if m.picker.cursor != 2 {
		t.Fatalf("cursor ran past the last row: %d", m.picker.cursor)
	}
}

func TestPicker_SwallowsOtherKeysWhileOpen(t *testing.T) {
	m := pickerModel(1)
	// A stray letter must not leak into the prompt while browsing.
	_, handled := m.handlePickerKey(keyPress("z").Key())
	if !handled {
		t.Fatal("picker must swallow unrelated keys while open")
	}
}

func TestPicker_InactiveDoesNotIntercept(t *testing.T) {
	m := newTestModel(nil, false)
	if _, handled := m.handlePickerKey(keyPress("j").Key()); handled {
		t.Fatal("closed picker must not intercept keys")
	}
}

func TestPicker_SelectedOutOfRange(t *testing.T) {
	m := pickerModel(0)
	if _, ok := m.selected(); ok {
		t.Fatal("empty list must not yield a selection")
	}
}

func TestUpdate_SessionsLoadedMsg(t *testing.T) {
	m := pickerModel(0)
	m.picker.loading = true
	updated, _ := m.Update(SessionsLoadedMsg{
		Sessions: []session.SessionMeta{{ID: "sess_a"}, {ID: "sess_b"}},
	})
	got := asModel(t, updated)
	if got.picker.loading {
		t.Fatal("loading should clear once the list arrives")
	}
	if len(got.picker.sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(got.picker.sessions))
	}
}

func TestUpdate_SessionOpenedMsg_ErrorKeepsPickerOpen(t *testing.T) {
	m := pickerModel(1)
	updated, _ := m.Update(SessionOpenedMsg{Err: fmt.Errorf("boom")})
	got := asModel(t, updated)
	if !got.picker.active {
		t.Fatal("a failed open should leave the picker open to retry")
	}
	if got.picker.err == nil {
		t.Fatal("expected the error to be recorded")
	}
}

func TestHandleSlashCommand_SessionsOpensPicker(t *testing.T) {
	m := newTestModel(nil, false)
	_, handled := m.handleSlashCommand("/sessions")
	if !handled {
		t.Fatal("expected /sessions to be handled")
	}
	if !m.picker.active {
		t.Fatal("expected the picker to open")
	}
}
