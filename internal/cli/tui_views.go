package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/ui"
	"github.com/charmbracelet/x/ansi"
)

// Width tiers: narrow terminals keep the name plus one state word.
const (
	tuiNarrow = 40
	tuiMedium = 60
)

func (m tuiModel) View() tea.View {
	view := tea.NewView(m.frame().Screen(m.screenView()))
	view.AltScreen = true
	return view
}

func (m tuiModel) frame() ui.Frame {
	return ui.Frame{Theme: m.theme, Width: m.width, Height: m.height}
}

func (m tuiModel) screenView() ui.Screen {
	s := m.currentScreen()
	if m.issued != nil && m.screen != screenReview {
		s.Context = "Pending action on " + m.issued.Flags.target
		s.Help = append([]string{"u pending action"}, s.Help...)
	}
	return s
}

func (m tuiModel) currentScreen() ui.Screen {
	switch m.screen {
	case screenActions:
		return m.actionsScreen()
	case screenReview:
		return m.reviewScreen()
	case screenApps:
		return m.appsScreen()
	case screenApp:
		return m.appScreen()
	case screenLogs:
		return m.logsScreen()
	case screenOperation:
		return m.operationScreen()
	default:
		return m.targetsScreen()
	}
}

func (m tuiModel) targetsScreen() ui.Screen {
	s := ui.Screen{Title: "brine", Help: m.help(keyRefresh(2), keyQuit)}
	switch {
	case m.targets.known() && len(m.targets.Value) == 0:
		s.Help = m.help(keyRefresh(0), keyQuit)
		s.Body = m.message("No enrolled targets in this config directory.", "Enroll one with brine enroll.")
	case m.targets.known():
		s.Context = count(len(m.targets.Value), "enrolled target")
		s.Help = m.help(keySelect, keyOpen, keyRefresh(2), keyQuit)
		rows := make([][]ui.Cell, len(m.targets.Value))
		for i, name := range m.targets.Value {
			rows[i] = []ui.Cell{{Text: name}}
		}
		s.Table = &ui.Table{Columns: []ui.Column{{Title: "TARGET", Min: 1, Max: 63, Priority: 1}}, Rows: rows, Selected: m.target}
	case m.targets.State == loadFailed:
		s.Body = m.message("Could not list enrolled targets.")
	default:
		s.Body = m.message("Reading enrolled targets…")
	}
	return withError(s, m.targets.Err)
}

func (m tuiModel) appsScreen() ui.Screen {
	back := keyQuit
	if len(m.back) > 0 {
		back = keyBack
	}
	s := ui.Screen{Title: "brine · " + m.flags.target, Help: m.help(keyRefresh(0), back)}
	switch {
	case m.apps.known() && len(m.apps.Value.Apps) == 0:
		s.Context = m.readContext(m.apps.State, m.apps.At, "0 apps")
		s.Body = m.message("No committed Brine apps on "+m.flags.target+".", "", "Deploy with: "+m.flags.command("deploy", "brine.toml"))
	case m.apps.known():
		s.Context = m.readContext(m.apps.State, m.apps.At, count(len(m.apps.Value.Apps), "app"))
		s.Help = m.help(keySelect, keyOpen, keyRefresh(2), back)
		s.Table = appsTable(m.apps.Value, m.selected, m.width)
	case m.apps.State == loadFailed:
		s.Body = m.message("Could not read apps from " + m.flags.target + ".")
	default:
		s.Body = m.message("Reading apps from " + m.flags.target + "…")
	}
	return withError(s, m.apps.Err)
}

func (m tuiModel) appScreen() ui.Screen {
	s := ui.Screen{Title: "brine · " + m.flags.target + " › " + m.app, Help: m.help(helpKey{"l logs", 0}, keyRefresh(1), keyBack)}
	s = withError(s, m.apps.Err)
	status, found := m.appStatus()
	switch {
	case found:
		s.Context = m.readContext(m.apps.State, m.apps.At, "")
		s.Body = appDetailLines(status, m.width)
		keys := []helpKey{{"↑↓ scroll", 0}, {"p plan", 0}, keyBack}
		if status.LastOperation != nil {
			keys = append(keys, helpKey{"t events", 1})
		}
		keys = append(keys, helpKey{"l logs", 1}, keyRefresh(1))
		s.Help = m.help(keys...)
		s.Body = topWindow(s.Body, m.detailOffset, m.frame().BodyRows(s))
	case m.apps.known():
		s.Context = m.readContext(m.apps.State, m.apps.At, "")
		s.Body = m.message(m.app + " is not in the latest report from " + m.flags.target + ".")
	case m.apps.State == loadFailed:
		s.Body = m.message("Could not read " + m.app + " from " + m.flags.target + ".")
	default:
		s.Body = m.message("Reading " + m.app + " from " + m.flags.target + "…")
	}
	return s
}

func (m tuiModel) appStatus() (apps.Status, bool) {
	for _, status := range m.apps.Value.Apps {
		if status.App == m.app {
			return status, true
		}
	}
	return apps.Status{}, false
}

func (m tuiModel) logsScreen() ui.Screen {
	flags, app := m.logSelection()
	readAgain := helpKey{"r read again", 0}
	s := ui.Screen{Title: "brine · " + flags.target + " › " + app + " › logs", Help: m.help(readAgain, keyBack)}
	s = withError(s, m.logs.Err)
	switch {
	case m.logs.known() && len(m.logs.Value) == 0:
		s.Context = m.readContext(m.logs.State, m.logs.At, "tail "+strconv.Itoa(tuiLogTail))
		s.Body = m.message("No log entries for " + app + ".")
	case m.logs.known():
		s.Context = m.readContext(m.logs.State, m.logs.At, "tail "+strconv.Itoa(tuiLogTail))
		s.Help = m.help(helpKey{"↑↓ scroll", 0}, helpKey{"r read again", 1}, keyBack)
		s.Body = m.logWindow(m.frame().BodyRows(s))
	case m.logs.State == loadFailed:
		s.Body = m.message("Could not read logs for " + app + ".")
	default:
		s.Body = m.message("Reading the last " + strconv.Itoa(tuiLogTail) + " log entries…")
	}
	return s
}

// message word-wraps plain sentences to the terminal width. Log entries and
// tables never wrap; they truncate so one item stays one row.
func (m tuiModel) message(lines ...string) []string {
	var out []string
	for _, line := range lines {
		out = append(out, strings.Split(ansi.Wrap(ui.Sanitize(line), max(1, m.width), ""), "\n")...)
	}
	return out
}

// logWindow shows the newest entries that fit, scrolled back by logOffset.
// One entry is one row: newlines are escaped before Screen sanitizes controls.
func (m tuiModel) logWindow(rows int) []string {
	lines := m.logs.Value
	end := len(lines) - max(0, min(m.logOffset, len(lines)-rows))
	start := max(0, end-rows)
	short := m.width < tuiMedium
	out := make([]string, 0, end-start)
	for _, line := range lines[start:end] {
		out = append(out, logText(line, short))
	}
	return out
}

func logText(line logs.Line, short bool) string {
	stamp := line.Timestamp
	if parsed, err := time.Parse(time.RFC3339Nano, stamp); short && err == nil {
		stamp = parsed.UTC().Format("15:04:05Z07:00")
	}
	return fmt.Sprintf("%s [%d] %s", stamp, line.Priority, strings.ReplaceAll(line.Message, "\n", `\n`))
}

// readContext is the header's right side: what was read and when. A refresh in
// flight keeps showing the last read's values and says so.
func (m tuiModel) readContext(state loadState, at time.Time, what string) string {
	when := "read " + at.In(m.loc).Format("15:04:05")
	if m.width < tuiMedium {
		when = at.In(m.loc).Format("15:04:05")
	}
	if state == loadLoading {
		when = "reading…"
	}
	if what == "" {
		return when
	}
	return what + " · " + when
}

// helpKey is one help item and the narrowest width tier that shows it:
// 0 always, 1 from tuiNarrow columns, 2 from tuiMedium columns.
type helpKey struct {
	text string
	tier int
}

var (
	keySelect = helpKey{"↑↓ select", 1}
	keyOpen   = helpKey{"enter open", 0}
	keyQuit   = helpKey{"q quit", 0}
	keyBack   = helpKey{"esc back", 0}
)

func keyRefresh(tier int) helpKey { return helpKey{"r refresh", tier} }

// help lists only keys that act in the current state, dropping lower-tier
// keys on narrow terminals; Screen drops whatever still does not fit.
func (m tuiModel) help(keys ...helpKey) []string {
	tier := 2
	switch {
	case m.width < tuiNarrow:
		tier = 0
	case m.width < tuiMedium:
		tier = 1
	}
	items := make([]string, 0, len(keys))
	for _, key := range keys {
		if key.tier <= tier {
			items = append(items, key.text)
		}
	}
	return items
}

func withError(s ui.Screen, err *result.Error) ui.Screen {
	if err != nil {
		s.Notice, s.NoticeTone = errorNotice(err), ui.Bad
	}
	return s
}

// errorNotice shows only the stable code and its catalog message, never a
// wrapped cause or transport diagnostics.
func errorNotice(err *result.Error) string {
	return "Error: " + string(err.Code()) + ". " + err.Error()
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// appsTable drops low-priority columns first: at 80 columns every column fits,
// at 50 the app, health and last operation remain, and under 40 the app and
// its last operation. Every state word fits in full from 40 columns up.
func appsTable(report apps.Report, selected, width int) *ui.Table {
	rows := make([][]ui.Cell, len(report.Apps))
	for i, a := range report.Apps {
		rows[i] = []ui.Cell{{Text: a.App}, unitCell(a.Health), healthCell(a.Health.Direct), releaseCell(a.Current), operationCell(a.LastOperation), driftCell(a.Drift.State)}
	}
	operation := len("recovery required")
	if width < tuiNarrow {
		operation = len("LAST OPERATION")
	}
	return &ui.Table{Columns: []ui.Column{
		{Title: "APP", Min: 12, Max: 24, Priority: 6},
		{Title: "UNIT", Min: 8, Max: 10, Priority: 2},
		{Title: "HEALTH", Min: 11, Max: 13, Priority: 4},
		{Title: "RELEASE", Min: 12, Max: 14, Priority: 1},
		{Title: "LAST OPERATION", Min: operation, Max: 19, Priority: 5},
		{Title: "DRIFT", Min: 7, Max: 8, Priority: 3},
	}, Rows: rows, Selected: selected}
}

// unitCell uses the observation's own word when the unit was not checked.
func unitCell(h apps.Health) ui.Cell {
	switch {
	case h.UnitActive.Value == nil:
		return ui.Cell{Text: "unknown", Tone: ui.Muted}
	case *h.UnitActive.Value:
		return ui.Cell{Text: "active"}
	default:
		return ui.Cell{Text: "inactive", Tone: ui.Warn}
	}
}

func healthCell(direct string) ui.Cell {
	tone := ui.Muted
	switch direct {
	case "healthy":
		tone = ui.Good
	case "unhealthy":
		tone = ui.Bad
	}
	return ui.Cell{Text: words(direct), Tone: tone}
}

func releaseCell(r *apps.Release) ui.Cell {
	if r == nil {
		return ui.Cell{Text: "none", Tone: ui.Muted}
	}
	return ui.Cell{Text: r.ID}
}

func operationCell(op *ops.Operation) ui.Cell {
	if op == nil {
		return ui.Cell{Text: "none", Tone: ui.Muted}
	}
	return ui.Cell{Text: words(string(op.State)), Tone: stateTone(op.State)}
}

func driftCell(state string) ui.Cell {
	tone := ui.Muted
	switch state {
	case "drifted":
		tone = ui.Warn
	case "in_sync":
		tone = ui.Plain
	}
	return ui.Cell{Text: words(state), Tone: tone}
}

// stateTone only reinforces the state word, which is always shown.
func stateTone(state ops.State) ui.Tone {
	switch state {
	case ops.Succeeded:
		return ui.Good
	case ops.Failed:
		return ui.Bad
	case ops.RolledBack, ops.RecoveryRequired, ops.LaunchUnknown:
		return ui.Warn
	default:
		return ui.Accent
	}
}

// words shows host vocabulary as brine status does, with underscores as spaces.
func words(text string) string { return strings.ReplaceAll(text, "_", " ") }

// appDetailLines lays out the same lines brine status prints as label and
// value columns, wrapping comma-separated values at the terminal width.
func appDetailLines(status apps.Status, width int) []string {
	var lines []string
	for _, line := range strings.Split(strings.Join(appStatusLines(status), "\n"), "\n") {
		if line != status.App {
			lines = append(lines, detailLines(status.App, line, width)...)
		}
	}
	return lines
}

const detailLabelWidth = 21

func detailLines(app, line string, width int) []string {
	label, value, ok := strings.Cut(strings.TrimSpace(line), ": ")
	if !ok {
		return []string{line}
	}
	if label == app {
		label = "Managed"
	}
	if unit, health, found := strings.Cut(value, "; direct health: "); label == "Unit" && found {
		lines := layoutDetail(label, unit, width)
		return append(lines, layoutDetail("Direct health", health, width)...)
	}
	return layoutDetail(label, value, width)
}

func layoutDetail(label, value string, width int) []string {
	if width < tuiMedium {
		return append([]string{label}, wrapValue("  ", value, width)...)
	}
	return wrapValue(fmt.Sprintf("%-*s", detailLabelWidth-1, label)+" ", value, width)
}

// shortDigest shows a digest with the 12 hex digits brine status shows for images.
func shortDigest(part string) string {
	i := strings.Index(part, "sha256:")
	if i < 0 || len(part) <= i+len("sha256:")+12 {
		return part
	}
	return part[:i+len("sha256:")+12] + "…"
}

// wrapValue puts the first row after prefix and indents the rest under it.
// A single part wider than the row is truncated with an ellipsis.
func wrapValue(prefix, value string, width int) []string {
	indent := strings.Repeat(" ", ansi.StringWidth(prefix))
	room := max(1, width-len(indent))
	var lines []string
	row := ""
	for _, part := range strings.Split(value, ", ") {
		part = ansi.Truncate(ui.Sanitize(shortDigest(part)), room, "…")
		if row != "" && ansi.StringWidth(row)+2+ansi.StringWidth(part) > room {
			lines = append(lines, row)
			row = ""
		}
		if row != "" {
			row += "  "
		}
		row += part
	}
	lines = append(lines, row)
	for i := range lines {
		lines[i] = indent + lines[i]
	}
	lines[0] = prefix + strings.TrimPrefix(lines[0], indent)
	return lines
}

func topWindow(lines []string, offset, rows int) []string {
	offset = max(0, min(offset, len(lines)-rows))
	start, end := ui.Window(len(lines), offset+rows-1, rows)
	return lines[start:end]
}

func (m tuiModel) clampDetailOffset(offset int) int {
	status, found := m.appStatus()
	if !found {
		return 0
	}
	rows := m.frame().BodyRows(m.appScreen())
	return max(0, min(offset, len(appDetailLines(status, m.width))-rows))
}
