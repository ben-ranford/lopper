//go:build !regressionproof

package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestStaveConsoleSummaryAltGrText(t *testing.T) {
	for _, modifiers := range []tea.KeyMod{0, tea.ModShift, tea.ModCtrl | tea.ModAlt} {
		m := newSummaryTerminalTest()
		m.insert("open ")
		m.Update(tea.KeyPressMsg{Code: 'q', Text: "@", Mod: modifiers})
		m.insert("scope/pkg")
		if got := string(m.line); got != "open @scope/pkg" {
			t.Fatalf("modifiers=%v: command=%q", modifiers, got)
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.err != nil || m.state.selectedDependency != "@scope/pkg" || len(m.line) != 0 {
			t.Fatalf("modifiers=%v: selected=%q line=%q err=%v", modifiers, m.state.selectedDependency, string(m.line), m.err)
		}
	}
	checkSummaryControlKeys(t)
}

func checkSummaryControlKeys(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		key    tea.KeyPressMsg
		want   string
		cursor int
		quit   bool
	}{
		{tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, "ab", 1, true},
		{tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}, "a", 1, false},
		{tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}, "ab", 0, false},
		{tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl}, "ab", 2, false},
		{tea.KeyPressMsg{Code: tea.KeyF1}, "ab", 1, false},
		{tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl | tea.ModAlt, Text: "\x00\x1b\n"}, "ab", 1, false},
	} {
		m := newSummaryTerminalTest()
		m.insert("ab")
		m.cursor = 1
		m.Update(tc.key)
		if string(m.line) != tc.want || m.cursor != tc.cursor || m.quit != tc.quit {
			t.Fatalf("key=%v: line=%q cursor=%d quit=%v", tc.key, string(m.line), m.cursor, m.quit)
		}
	}
	m := newSummaryTerminalTest()
	m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if !m.quit {
		t.Fatal("Ctrl+D on empty input must still quit")
	}
}
