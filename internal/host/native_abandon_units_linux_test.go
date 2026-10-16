//go:build linux

package host

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type abandonUnitStops struct {
	names []string
	err   error
}

func (s *abandonUnitStops) Stop(_ context.Context, u systemd.Unit) error {
	s.names = append(s.names, u.String())
	return s.err
}

func TestNativeAbandonCancelsBootRestartJobsBeforeObservation(t *testing.T) {
	binding := data.ReplicaBindingID(strings.Repeat("a", 32))
	expected := store.NativeAbandonExpected{App: "fixture", Permit: store.EnginePermit{Runtime: data.EngineRuntime{BindingID: binding}}, Release: store.Release{Managed: &ops.ManagedRelease{}}}
	engine := "brine-engine-" + string(binding) + ".service"
	for _, frontend := range []bool{false, true} {
		if frontend {
			expected.Release.Managed.Frontend = &ops.ManagedFrontend{}
		}
		stopper := &abandonUnitStops{}
		if err := stopNativeAbandonUnits(t.Context(), stopper, expected); err != nil {
			t.Fatal(err)
		}
		wanted := []string{engine}
		if frontend {
			wanted = []string{"fixture.service", engine}
		}
		if !reflect.DeepEqual(stopper.names, wanted) {
			t.Fatal("owned boot restart jobs were not canceled", stopper.names, wanted)
		}
	}
	stopper := &abandonUnitStops{err: errors.New("unknown stop")}
	if err := stopNativeAbandonUnits(t.Context(), stopper, expected); err == nil || len(stopper.names) != 1 {
		t.Fatal("unknown stop admitted further work", err, stopper.names)
	}
}
