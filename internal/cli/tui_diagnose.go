package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/diagnose"
)

type diagnosisMsg struct {
	token  uint64
	report diagnose.Report
	lines  []string
	err    error
}

func (m tuiModel) openDiagnosis() (tea.Model, tea.Cmd) {
	flags, app := m.flags, ""
	switch m.screen {
	case screenApp:
		app = m.app
	case screenOperation:
		if m.observe == nil || !m.observe.Bound || m.observe.Operation.App == "" {
			return m, nil
		}
		flags, app = m.observe.Flags, m.observe.Operation.App
	case screenApps:
	default:
		return m, nil
	}
	if flags != m.diagnosisFlags || app != m.diagnosisApp {
		m.diagnosis, m.diagnosisText = loadable[diagnose.Report]{}, nil
	}
	m.diagnosisFlags, m.diagnosisApp, m.diagnosisOffset = flags, app, 0
	m = m.push(screenDiagnosis)
	return m.loadDiagnosis()
}

func (m tuiModel) loadDiagnosis() (tuiModel, tea.Cmd) {
	flags, app := m.diagnosisFlags, m.diagnosisApp
	host := m.connect(flags)
	m, ctx, token := m.beginRead()
	m.diagnosis.start()
	return m, func() tea.Msg {
		report, err := host.Diagnose(ctx, app)
		var lines []string
		if err == nil {
			lines, err = diagnosisLines(report, flags)
		}
		return diagnosisMsg{token: token, report: report, lines: lines, err: err}
	}
}

func (m tuiModel) diagnosisReply(msg diagnosisMsg) (tea.Model, tea.Cmd) {
	if m.screen != screenDiagnosis || !m.current(msg.token) {
		return m, nil
	}
	m.diagnosis.finish(msg.report, msg.err, m.now())
	if msg.err == nil {
		m.diagnosisText = msg.lines
	}
	m.diagnosisOffset = m.clampDiagnosisOffset(m.diagnosisOffset)
	return m, nil
}

const diagnosisSummaryLimit = 6

func (m tuiModel) diagnosisExitNotice() string {
	scope := "host"
	args := []string{"diagnose"}
	if m.diagnosisApp != "" {
		scope = "app " + m.diagnosisApp
		args = append(args, m.diagnosisApp)
	}
	status := "Diagnosis was not obtained."
	if m.diagnosis.known() {
		status = m.diagnosisSummary()
	}
	return fmt.Sprintf("Diagnosis for %s on %s. %s Full report: %s", scope, m.diagnosisFlags.target, status, m.diagnosisFlags.command(args...))
}

func (m tuiModel) diagnosisSummary() string {
	findings := m.diagnosis.Value.Findings
	codes := make([]string, 0, min(len(findings), diagnosisSummaryLimit))
	for _, finding := range findings[:min(len(findings), diagnosisSummaryLimit)] {
		codes = append(codes, finding.Code)
	}
	text := "No failure rule matched. Unknown facts are not evidence of health."
	if len(codes) > 0 {
		text = "Findings: " + strings.Join(codes, ", ") + "."
	}
	if omitted := len(findings) - len(codes); omitted > 0 {
		text += fmt.Sprintf(" %d more findings omitted.", omitted)
	}
	if m.diagnosis.State != loadReady {
		text = "Last report is stale. " + text
	}
	return text + " Collection is not a health verdict."
}
