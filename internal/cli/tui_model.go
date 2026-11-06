package cli

import (
	"context"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/apps"
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

// tuiModel is the read-only terminal UI. Every value it shows was read from
// the selected target in this session; there is no sample or demo data.
type tuiModel struct {
	ctx          context.Context // session context; canceled when the runner returns
	connect      func(operationFlags) tuiHost
	listTargets  func() ([]string, error)
	configDir    string
	flags        operationFlags // current target selection; empty on the picker
	host         tuiHost
	screen       tuiScreen
	back         []tuiScreen
	width        int
	height       int
	theme        ui.Theme
	token        uint64             // bumped on every navigation and read
	cancelRead   context.CancelFunc // the one current read, if any
	initial      tea.Cmd            // the first read, returned once by Init
	targets      loadable[[]string]
	target       int // picker selection
	apps         loadable[apps.Report]
	selected     int // app list selection
	app          string
	logs         loadable[[]logs.Line]
	logOffset    int // entries scrolled back from the newest
	detailOffset int // app detail rows scrolled from the top
	observe      *tuiObservation
	after        func(time.Duration, tea.Msg) tea.Cmd
	now          func() time.Time
	loc          *time.Location
}

// newTUIModel starts on the app list for an explicit target, or on the target
// picker otherwise. The first read starts when the runner calls Init.
func newTUIModel(ctx context.Context, deps Dependencies, flags operationFlags) tuiModel {
	m := tuiModel{
		ctx:         ctx,
		connect:     func(f operationFlags) tuiHost { return connectedHost{flags: f, deps: deps} },
		listTargets: func() ([]string, error) { return listTargets(flags) },
		configDir:   flags.configDir,
		width:       80,
		height:      24,
		theme:       ui.ThemeFromEnv(),
		now:         time.Now,
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
	m.flags, m.host, m.screen = flags, m.connect(flags), screenApps
	m, m.initial = m.loadApps()
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
	case "r":
		return m.refresh()
	case "enter":
		return m.enter()
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
	host, app := m.host, m.app
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
	return m.reload()
}

// reload restarts a read that navigation abandoned before it ever succeeded.
func (m tuiModel) reload() (tea.Model, tea.Cmd) {
	switch {
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
	m = m.push(screenApps)
	return m.loadApps()
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
	m.logs, m.logOffset = loadable[[]logs.Line]{}, 0
	m = m.push(screenLogs)
	return m.loadLogs()
}

func (m tuiModel) move(delta int) tuiModel {
	switch m.screen {
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
