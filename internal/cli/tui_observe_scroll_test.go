package cli

import (
	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/ops"
	"testing"
)

func TestReviewOperationScrollReversesAtBottom(t *testing.T) {
	m := operationGolden(ops.Starting)
	t.Cleanup(m.stopRead)
	m.width, m.height = 30, 8
	m.observe.Events = operationFixture(ops.Starting, 0, 32).Events
	for range 100 {
		m = m.move(1)
	}
	before := view(m)
	offset := m.observe.Offset
	m = m.move(-1)
	if view(m) == before {
		t.Fatalf("up did not scroll from the visible bottom: stored offset %d -> %d, total rows %d, body rows %d\n%s", offset, m.observe.Offset, len(m.operationLines()), m.frame().BodyRows(m.operationScreen()), before)
	}
}

func TestReviewOperationScrollClampsAfterResize(t *testing.T) {
	m := operationGolden(ops.Starting)
	t.Cleanup(m.stopRead)
	m.width, m.height = 30, 8
	m.observe.Events = operationFixture(ops.Starting, 0, 32).Events
	for range 100 {
		m = m.move(1)
	}
	m, _ = updateTUI(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	before := view(m)
	m = m.move(-1)
	if view(m) == before {
		t.Fatal("resizing left a hidden offset: up no longer moves the visible timeline")
	}
}
