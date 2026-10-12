//go:build linux

package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
	"testing"
)

func TestNativeRestoreContainerAbsenceRejectsFrontendAndPrivateReaders(t *testing.T) {
	binding := data.ReplicaBindingID("0123456789abcdef0123456789abcdef")
	for _, tc := range []struct {
		name    string
		labels  map[string]string
		running bool
		pid     int
		want    bool
	}{
		{"unrelated", map[string]string{}, true, 5, true},
		{engineContainerName(binding), map[string]string{}, false, 0, true},
		{engineContainerName(binding), map[string]string{}, true, 5, false},
		{"renamed", map[string]string{"BRINE_ENGINE_BINDING": string(binding)}, true, 5, false},
		{"systemd-game", map[string]string{}, true, 5, false},
		{"systemd-game", map[string]string{}, false, 7, false},
		{"brine-native-read-owned", map[string]string{"BRINE_PRIVATE_VERIFY": "owned"}, false, 0, false},
		{"renamed", map[string]string{"BRINE_MODULE_STAGE": "owned"}, false, 0, false},
	} {
		c := engineContainer{Name: tc.name}
		c.Config.Labels = tc.labels
		c.State.Running = &tc.running
		c.State.PID = &tc.pid
		if got := nativeRestoreContainersAbsent([]engineContainer{c}, binding, "game"); got != tc.want {
			t.Fatalf("%s absence=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestNativeRestoreEffectCannotUseDecodedCapabilities(t *testing.T) {
	ctx := context.Background()
	r := nativeBackupRuntime{}
	if _, err := r.executeNativeLiveRestore(ctx, nil, &store.EnginePreparation{}, &data.EngineLock{}, nil); err == nil {
		t.Fatal("decoded stage admitted effects")
	}
	if _, err := r.observeNativeRestoreTransition(ctx, store.NativeRestoreTransitionView{}, &engineTransitionTree{}, nil); err == nil {
		t.Fatal("decoded layout minted completion")
	}
}
