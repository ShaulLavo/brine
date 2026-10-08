package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/ShaulLavo/brine/internal/ui"
)

func TestWelcomeResize(t *testing.T) {
	m := welcomeModel{frame: ui.Frame{Theme: ui.NewTheme(true), Width: 80, Height: 24}}
	for _, size := range []tea.WindowSizeMsg{{Width: 24, Height: 8}, {Width: 80, Height: 24}} {
		updated, cmd := m.Update(size)
		if cmd != nil {
			t.Fatal("resize unexpectedly returned a command")
		}
		m = updated.(welcomeModel)
		view := m.View().Content
		if lipgloss.Width(view) > size.Width || lipgloss.Height(view) > size.Height {
			t.Fatalf("view exceeds %v: %q", size, view)
		}
		if !strings.Contains(view, "Press q to quit.") {
			t.Fatalf("quit help missing: %q", view)
		}
		if size.Width == 80 && !strings.Contains(view, "A foundation, not yet a deployment engine.") {
			t.Fatalf("welcome content missing after resize: %q", view)
		}
	}
}

func TestWelcomeQuitKeys(t *testing.T) {
	m := welcomeModel{}
	for _, key := range []tea.Key{
		{Code: 'q', Text: "q"},
		{Code: 'c', Mod: tea.ModCtrl},
		{Code: tea.KeyEscape},
	} {
		_, cmd := m.Update(tea.KeyPressMsg(key))
		if cmd == nil {
			t.Fatalf("%s did not quit", key.String())
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%s returned something other than QuitMsg", key.String())
		}
	}
	for _, msg := range []tea.Msg{tea.KeyPressMsg{Code: 'a', Text: "a"}, tea.KeyReleaseMsg{Code: 'q', Text: "q"}} {
		if _, cmd := m.Update(msg); cmd != nil {
			t.Fatalf("%v unexpectedly quit", msg)
		}
	}
}
