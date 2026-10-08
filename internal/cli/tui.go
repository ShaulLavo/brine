package cli

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

type welcomeModel struct{}

func (welcomeModel) Init() tea.Cmd { return nil }
func (m welcomeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}
	}
	return m, nil
}
func (welcomeModel) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Render("brine  /  deployment control")
	return title + "\n\nA foundation, not yet a deployment engine.\n\nPress q to quit.\n"
}

func newTUICmd(jsonOutput, noInput *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open the interactive terminal UI",
		RunE: func(cmd *cobra.Command, args []string) error {
			if *jsonOutput || *noInput {
				return fmt.Errorf("tui is interactive; remove --json and --no-input")
			}
			_, err := tea.NewProgram(welcomeModel{}).Run()
			return err
		},
	}
}
