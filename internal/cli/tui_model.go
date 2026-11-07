package cli

import (
	"context"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/ui"
)

type tuiScreen uint8

const (
	screenTargets tuiScreen = iota
	screenApps
	screenApp
	screenLogs
	screenOperation
	screenActions
	screenReview
	screenDiagnosis
)

type loadState uint8

const (
	loadIdle loadState = iota
	loadLoading
	loadReady
	loadFailed
)

// loadable is one read's view state. A failed or abandoned read keeps the
// last good value and its read time, so views can show it as such.
type loadable[T any] struct {
	State loadState
	Value T
	Err   *result.Error // rendered as code and catalog message only
	At    time.Time
}

func (l *loadable[T]) start() {
	l.State, l.Err = loadLoading, nil
}

func (l *loadable[T]) finish(value T, err error, at time.Time) {
	if err != nil {
		l.State, l.Err = loadFailed, result.Classify(err)
		return
	}
	l.State, l.Value, l.At = loadReady, value, at
}

// abandon returns a canceled read to its last known state.
func (l *loadable[T]) abandon() {
	if l.State != loadLoading {
		return
	}
	l.State = loadIdle
	if !l.At.IsZero() {
		l.State = loadReady
	}
}

func (l loadable[T]) known() bool { return !l.At.IsZero() }

type targetsMsg struct {
	token uint64
	names []string
	err   error
}

type appsMsg struct {
	token  uint64
	report apps.Report
	err    error
}

type logsMsg struct {
	token uint64
	lines []logs.Line
	err   error
}

// tuiModel is the terminal UI. Every value it shows was read from
// the selected target in this session; there is no sample or demo data.
type tuiModel struct {
	diagnosis       loadable[diagnose.Report]
	diagnosisFlags  operationFlags
	diagnosisApp    string
	diagnosisText   []string
	diagnosisOffset int
	diagnosisExit   string
	entry           tuiEntry
	review          loadable[tuiReview]
	reviewFlags     operationFlags
	actionIndex     int
	reviewOffset    int
	confirm         bool
	issued          *issuedApply
	nextIssued      uint64
	newKey          func() (string, error)
	ctx             context.Context // session context; canceled when the runner returns
	connect         func(operationFlags) tuiHost
	listTargets     func() ([]string, error)
	configDir       string
	flags           operationFlags // current target selection; empty on the picker
	host            tuiHost
	screen          tuiScreen
	back            []tuiScreen
	width           int
	height          int
	theme           ui.Theme
	token           uint64             // bumped on every navigation and read
	cancelRead      context.CancelFunc // the one current read, if any
	initial         tea.Cmd            // the first read, returned once by Init
	targets         loadable[[]string]
	target          int // picker selection
	apps            loadable[apps.Report]
	selected        int // app list selection
	app             string
	logs            loadable[[]logs.Line]
	logFlags        operationFlags
	logApp          string
	logOffset       int // entries scrolled back from the newest
	detailOffset    int // app detail rows scrolled from the top
	observe         *tuiObservation
	after           func(time.Duration, tea.Msg) tea.Cmd
	now             func() time.Time
	loc             *time.Location
}

// newTUIModel starts the selected entry for an explicit target, or the target
// picker otherwise. The first read starts when the runner calls Init.
func newTUIModel(ctx context.Context, deps Dependencies, flags operationFlags, entry tuiEntry) tuiModel {
	m := tuiModel{
		entry:       entry,
		ctx:         ctx,
		connect:     func(f operationFlags) tuiHost { return connectedHost{flags: f, deps: deps} },
		listTargets: func() ([]string, error) { return listTargets(flags) },
		configDir:   flags.configDir,
		width:       80,
		height:      24,
		theme:       ui.ThemeFromEnv(),
		now:         time.Now,
		newKey:      randomOperationKey,
		after:       tuiAfter,
		loc:         time.Local,
	}
	return m.begin(flags) //nolint:contextcheck // The model keeps ctx as its session context; every read derives from it.
}

func (m tuiModel) begin(flags operationFlags) tuiModel {
	if flags.target == "" {
		m, m.initial = m.loadTargets()
		return m
	}
	m.flags, m.host = flags, m.connect(flags)
	m, m.initial = m.loadEntry()
	return m
}

func (m tuiModel) Init() tea.Cmd { return m.initial }

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.logOffset = m.clampLogOffset(m.logOffset)
		m.detailOffset = m.clampDetailOffset(m.detailOffset)
		m.clampOperationOffset()
		m.reviewOffset = m.clampReviewOffset(m.reviewOffset)
		m.diagnosisOffset = m.clampDiagnosisOffset(m.diagnosisOffset)
	case tea.KeyPressMsg:
		return m.key(msg.String())
	case targetsMsg:
		if m.current(msg.token) {
			m.targets.finish(msg.names, msg.err, m.now())
			m.target = clampIndex(m.target, len(m.targets.Value))
		}
	case appsMsg:
		if m.current(msg.token) {
			m.apps.finish(msg.report, msg.err, m.now())
			m.selected = clampIndex(m.selected, len(m.apps.Value.Apps))
		}
	case reviewMsg:
		if m.current(msg.token) {
			m.review.finish(msg.review, msg.err, m.now())
			m.reviewOffset = m.clampReviewOffset(m.reviewOffset)
		}
	case diagnosisMsg:
		return m.diagnosisReply(msg)
	case acceptMsg:
		return m.acceptance(msg)
	case pageMsg:
		return m.operationPage(msg)
	case pollMsg:
		if msg.token == m.token && m.screen == screenOperation {
			return m.loadOperation()
		}
	case logsMsg:
		if m.current(msg.token) {
			m.logs.finish(msg.lines, msg.err, m.now())
			m.logOffset = m.clampLogOffset(m.logOffset)
		}
	}
	return m, nil
}

// current reports whether a read result belongs to the current read, and
// releases that read's context. Stale results are dropped unseen.
func (m *tuiModel) current(token uint64) bool {
	if token != m.token {
		return false
	}
	if m.cancelRead != nil {
		m.cancelRead()
		m.cancelRead = nil
	}
	return true
}

func (m tuiModel) key(key string) (tea.Model, tea.Cmd) {
	if m.confirm {
		if key == "y" {
			return m.issueApply()
		}
		m.confirm = false
		m.reviewOffset = m.clampReviewOffset(m.reviewOffset)
		if key != "ctrl+c" {
			return m, nil
		}
	}
	switch key {
	case "ctrl+c":
		return m.quit()
	case "q":
		if len(m.back) == 0 {
			return m.quit()
		}
		return m.goBack()
	case "esc", "backspace":
		return m.goBack()
	case "up", "k":
		return m.move(-1), nil
	case "down", "j":
		return m.move(1), nil
	case "d":
		return m.openDiagnosis()
	case "x":
		if m.screen == screenDiagnosis {
			m.diagnosisExit = m.diagnosisExitNotice()
			return m.quit()
		}
	case "r":
		return m.refresh()
	case "enter":
		return m.enter()
	case "a":
		return m.confirmApply()
	case "p":
		if m.screen == screenApp {
			return m.openActions()
		}
	case "u":
		return m.openIssued()
	case "t":
		if m.screen == screenApp {
			return m.openOperation()
		}
	case "l":
		if m.screen == screenApp || m.screen == screenOperation {
			return m.openLogs()
		}
	}
	return m, nil
}

func (m tuiModel) quit() (tea.Model, tea.Cmd) {
	m.stopRead()
	return m, tea.Quit
}

// stopRead cancels the current read's context and invalidates its token.
func (m *tuiModel) stopRead() {
	if m.cancelRead != nil {
		m.cancelRead()
		m.cancelRead = nil
	}
	m.token++
	m.targets.abandon()
	m.apps.abandon()
	m.logs.abandon()
	m.review.abandon()
	m.diagnosis.abandon()
	m.confirm = false
	if m.observe != nil {
		m.observe.Loading = false
	}
}

// beginRead replaces the current read. Only one read is ever in flight.
func (m tuiModel) beginRead() (tuiModel, context.Context, uint64) {
	m.stopRead()
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelRead = cancel
	return m, ctx, m.token
}

func (m tuiModel) loadTargets() (tuiModel, tea.Cmd) {
	list := m.listTargets
	m, _, token := m.beginRead() // a local directory listing has no cancelable I/O
	m.targets.start()
	return m, func() tea.Msg {
		names, err := list()
		return targetsMsg{token: token, names: names, err: err}
	}
}

func (m tuiModel) loadApps() (tuiModel, tea.Cmd) {
	host := m.host
	m, ctx, token := m.beginRead()
	m.apps.start()
	return m, func() tea.Msg {
		report, err := host.Apps(ctx)
		return appsMsg{token: token, report: report, err: err}
	}
}

func (m tuiModel) loadLogs() (tuiModel, tea.Cmd) {
	flags, app := m.logSelection()
	host := m.host
	if flags != m.flags {
		host = m.connect(flags)
	}
	m, ctx, token := m.beginRead()
	m.logs.start()
	return m, func() tea.Msg {
		lines, err := host.Logs(ctx, app)
		return logsMsg{token: token, lines: lines, err: err}
	}
}

func (m tuiModel) push(next tuiScreen) tuiModel {
	m.back = append(slices.Clip(m.back), m.screen)
	m.screen = next
	return m
}

func (m tuiModel) goBack() (tea.Model, tea.Cmd) {
	if len(m.back) == 0 {
		return m, nil
	}
	m.stopRead()
	last := len(m.back) - 1
	m.screen, m.back = m.back[last], m.back[:last]
	m.reviewOffset = m.clampReviewOffset(m.reviewOffset)
	return m.reload()
}

// reload restarts a read that navigation abandoned before it ever succeeded.
func (m tuiModel) reload() (tea.Model, tea.Cmd) {
	switch {
	case m.screen == screenDiagnosis:
		if m.diagnosis.State == loadIdle {
			return m.loadDiagnosis()
		}
		return m, nil
	case m.screen == screenReview:
		if m.review.State == loadIdle {
			return m.loadReview()
		}
		return m, nil
	case m.screen == screenOperation:
		return m.loadOperation()
	case m.screen == screenTargets && m.targets.State == loadIdle:
		return m.loadTargets()
	case m.screen != screenTargets && m.apps.State == loadIdle:
		return m.loadApps()
	}
	return m, nil
}

func (m tuiModel) refresh() (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenDiagnosis:
		return m.loadDiagnosis()
	case screenReview:
		return m.loadReview()
	case screenActions:
		return m, nil
	case screenTargets:
		return m.loadTargets()
	case screenLogs:
		return m.loadLogs()
	case screenOperation:
		m.observe.Failures, m.observe.Err, m.observe.Phase = 0, nil, observePolling
		return m.loadOperation()
	default:
		return m.loadApps()
	}
}

func (m tuiModel) enter() (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenActions:
		return m.planAction()
	case screenTargets:
		return m.openTarget()
	case screenApps:
		return m.openApp()
	case screenApp:
		return m.openOperation()
	}
	return m, nil
}

func (m tuiModel) openTarget() (tea.Model, tea.Cmd) {
	if !m.targets.known() || len(m.targets.Value) == 0 {
		return m, nil
	}
	m.flags = operationFlags{target: m.targets.Value[m.target], configDir: m.configDir}
	m.host = m.connect(m.flags)
	m.apps, m.selected = loadable[apps.Report]{}, 0
	m.back = append(slices.Clip(m.back), m.screen)
	return m.loadEntry()
}

func (m tuiModel) openApp() (tea.Model, tea.Cmd) {
	if !m.apps.known() || len(m.apps.Value.Apps) == 0 {
		return m, nil
	}
	m.stopRead()
	m.app = m.apps.Value.Apps[m.selected].App
	m.detailOffset = 0
	return m.push(screenApp), nil
}

func (m tuiModel) openLogs() (tea.Model, tea.Cmd) {
	m.logFlags, m.logApp = m.flags, m.app
	if m.screen == screenOperation {
		m.logFlags, m.logApp = m.observe.Flags, m.observe.Operation.App
	}
	m.logs, m.logOffset = loadable[[]logs.Line]{}, 0
	m = m.push(screenLogs)
	return m.loadLogs()
}

func (m tuiModel) move(delta int) tuiModel {
	switch m.screen {
	case screenDiagnosis:
		m.diagnosisOffset = m.clampDiagnosisOffset(m.diagnosisOffset + delta)
	case screenActions:
		m.actionIndex = clampIndex(m.actionIndex+delta, len(m.actions()))
	case screenReview:
		m.reviewOffset = m.clampReviewOffset(m.reviewOffset + delta)
	case screenTargets:
		m.target = clampIndex(m.target+delta, len(m.targets.Value))
	case screenApps:
		m.selected = clampIndex(m.selected+delta, len(m.apps.Value.Apps))
	case screenApp:
		m.detailOffset = m.clampDetailOffset(m.detailOffset + delta)
	case screenOperation:
		m.observe.Offset += delta
		m.clampOperationOffset()
	case screenLogs:
		m.logOffset = m.clampLogOffset(m.logOffset - delta)
	}
	return m
}

// clampLogOffset keeps the log window full: it never scrolls past the oldest entry.
func (m tuiModel) clampLogOffset(offset int) int {
	rows := m.frame().BodyRows(m.logsScreen())
	return max(0, min(offset, len(m.logs.Value)-rows))
}

func clampIndex(i, n int) int {
	return max(0, min(i, n-1))
}

func (m tuiModel) logSelection() (operationFlags, string) {
	if m.logApp == "" {
		return m.flags, m.app
	}
	return m.logFlags, m.logApp
}
