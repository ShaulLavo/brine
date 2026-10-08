package cli

import (
	"context"
	"fmt"
	"io"

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

func newTUICmd(jsonOutput, noInput *bool, runTUI func(context.Context, io.Reader, io.Writer) error) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open the interactive terminal UI",
		RunE: func(cmd *cobra.Command, args []string) error {
			if *jsonOutput || *noInput {
				return fmt.Errorf("tui is interactive; remove --json and --no-input")
			}
			return runTUI(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}

// RunTUI runs the welcome screen using the caller's context and output.
// A nil input preserves Bubble Tea's process-input and controlling-terminal fallback.
func RunTUI(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithOutput(stdout), tea.WithoutSignalHandler()}
	if stdin != nil {
		opts = append(opts, tea.WithInput(stdin))
	}
	_, err := tea.NewProgram(welcomeModel{}, opts...).Run()
	return err
}
