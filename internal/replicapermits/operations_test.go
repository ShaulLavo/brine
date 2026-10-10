package replicapermits

import (
	"context"
	"errors"
	"testing"

	"github.com/ShaulLavo/brine/internal/systemd"
)

type operationUnitsFake struct {
	properties systemd.Properties
	err        error
	reads      int
	unit       string
}

func (f *operationUnitsFake) Show(_ context.Context, u systemd.Unit) (systemd.Properties, error) {
	f.reads++
	f.unit = u.String()
	return f.properties, f.err
}
func TestOperationActiveRequiresExactLiveTransientService(t *testing.T) {
	f := &operationUnitsFake{properties: systemd.Properties{ActiveState: "active", SubState: "running"}}
	o := Operations{Units: f}
	active, err := o.OperationActive(context.Background(), "operation")
	if !active || err != nil || f.unit != "brine-op-operation.service" {
		t.Fatal(active, err, f.unit)
	}
	for _, p := range []systemd.Properties{{ActiveState: "inactive", SubState: "dead"}, {ActiveState: "active", SubState: "exited"}, {ActiveState: "activating", SubState: "start"}, {ActiveState: "reloading", SubState: "running"}, {}} {
		f.properties = p
		if active, _ := o.OperationActive(context.Background(), "operation"); active {
			t.Fatal("non-running operation admitted", p)
		}
	}
	f.err = errors.New("unknown")
	if active, err := o.OperationActive(context.Background(), "operation"); active || err == nil {
		t.Fatal("unknown service admitted")
	}
	reads := f.reads
	if active, err := o.OperationActive(context.Background(), "../../foreign"); active || err == nil || f.reads != reads {
		t.Fatal("untyped operation reached subprocess")
	}
}
