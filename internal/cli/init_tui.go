package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/ui"
	"github.com/charmbracelet/x/ansi"
)

type initPhase uint8

const (
	initReview initPhase = iota
	initConfirm
	initApproved
	initCancelled
	initInterrupted
)

type initModel struct {
	preview               initPreview
	theme                 ui.Theme
	width, height, offset int
	phase                 initPhase
}

func newInitModel(preview initPreview, theme ui.Theme) initModel {
	return initModel{preview: preview, theme: theme, width: 80, height: 24}
}

func (initModel) Init() tea.Cmd { return nil }

func (m initModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
	case tea.KeyPressMsg:
		return m.key(message.String())
	}
	return m, nil
}

func (m initModel) key(key string) (tea.Model, tea.Cmd) {
	if key == "ctrl+c" {
		m.phase = initInterrupted
		return m, tea.Quit
	}
	if key == "q" || key == "esc" {
		m.phase = initCancelled
		return m, tea.Quit
	}
	if m.phase == initConfirm {
		if key == "y" && m.frame().NoticeVisible(m.screen()) {
			m.phase = initApproved
			return m, tea.Quit
		}
		m.phase = initReview
		return m, nil
	}
	switch key {
	case "a":
		m.phase = initConfirm
	case "down", "j":
		m.offset = min(m.offset+1, max(0, len(m.lines())-m.frame().BodyRows(m.screen())))
	case "up", "k":
		m.offset = max(0, m.offset-1)
	}
	return m, nil
}

func (m initModel) frame() ui.Frame {
	return ui.Frame{Theme: m.theme, Width: m.width, Height: m.height}
}

func (m initModel) lines() []string {
	text := "Create " + filepath.Join(m.preview.Directory, initFileName) + "\nNo host changes. No image or host-policy verification.\n\n" + string(m.preview.Content)
	lines := []string{}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		lines = append(lines, strings.Split(ansi.Wrap(ui.Sanitize(line), max(1, m.width), ""), "\n")...)
	}
	return lines
}

func (m initModel) screen() ui.Screen {
	s := ui.Screen{Title: "brine · init", Context: "local preview", Body: m.lines(), Help: []string{"a create", "↑↓ scroll", "esc/q cancel"}}
	if m.phase == initConfirm {
		s.Notice = fmt.Sprintf("Confirm: create %s for %s? y create · any other key cancels", filepath.Join(m.preview.Directory, initFileName), m.preview.App.Name)
		s.NoticeTone = ui.Warn
		s.Help = []string{"y create", "esc/q cancel"}
		if !m.frame().NoticeVisible(s) {
			s.Help = []string{"resize to confirm", "esc/q cancel"}
		}
	}
	s.Body = s.Body[min(m.offset, len(s.Body)):]
	return s
}

func (m initModel) View() tea.View {
	view := tea.NewView(m.frame().Screen(m.screen()))
	view.AltScreen = true
	return view
}
