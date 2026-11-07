package cli

import (
	"strconv"

	"github.com/ShaulLavo/brine/internal/ui"
)

func (m tuiModel) diagnosisScreen() ui.Screen {
	scope := m.diagnosisApp
	if scope == "" {
		scope = "host"
	}
	s := ui.Screen{
		Title: "brine · " + m.diagnosisFlags.target + " › " + scope + " › diagnosis",
		Help:  m.help(helpKey{"↑↓ scroll", 0}, helpKey{"x exit", 0}, keyBack, helpKey{"r read", 1}),
	}
	s = withError(s, m.diagnosis.Err)
	if !m.diagnosis.known() {
		s.Body = m.message("Reading diagnosis…")
		if m.diagnosis.State == loadFailed {
			s.Body = m.message("Diagnosis was not obtained. r retries the same scope.")
		}
		return s
	}
	s.Context = m.diagnosisContext()
	if m.diagnosis.State != loadReady {
		s.Notice, s.NoticeTone = "Last report is stale, read "+m.diagnosis.At.In(m.loc).Format("15:04:05")+".", ui.Warn
		if m.diagnosis.Err != nil {
			s.Notice += " " + errorNotice(m.diagnosis.Err)
		} else {
			s.Notice += " Reading diagnosis…"
		}
	}
	lines := m.message(m.diagnosisText...)
	s.Body = topWindow(lines, m.diagnosisOffset, m.frame().BodyRows(s))
	return s
}

func (m tuiModel) diagnosisContext() string {
	status := "read " + m.diagnosis.At.In(m.loc).Format("15:04:05")
	if m.diagnosis.State != loadReady {
		status = "stale · " + status
	}
	return strconv.Itoa(len(m.diagnosis.Value.Findings)) + " findings · " + status
}

func (m tuiModel) clampDiagnosisOffset(offset int) int {
	rows := m.frame().BodyRows(m.diagnosisScreen())
	lines := m.message(m.diagnosisText...)
	return max(0, min(offset, len(lines)-rows))
}
