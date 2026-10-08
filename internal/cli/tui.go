package cli

import (
	"context"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/ui"
	"github.com/spf13/cobra"
)

type welcomeModel struct {
	frame ui.Frame
}

func (welcomeModel) Init() tea.Cmd { return nil }
func (m welcomeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.frame.Width, m.frame.Height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}
	}
	return m, nil
}
func (m welcomeModel) View() tea.View {
	return tea.NewView(m.frame.Render(
		"brine  /  deployment control",
		"A foundation, not yet a deployment engine.",
		"Press q to quit.  ctrl+c / esc also quit.",
	))
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
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithOutput(stdout), tea.WithoutSignalHandler(), tea.WithWindowSize(80, 24)}
	if stdin != nil {
		opts = append(opts, tea.WithInput(stdin))
	}
	_, err := tea.NewProgram(welcomeModel{frame: ui.Frame{Theme: ui.ThemeFromEnv(), Width: 80, Height: 24}}, opts...).Run()
	return err
}
