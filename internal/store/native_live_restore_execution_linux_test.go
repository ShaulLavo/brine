//go:build linux

package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNativeLiveRestoreExecutionCannotComeFromJournalOrDecodedCapability(t *testing.T) {
	f, _, claim := storedNativeRestoreJournalFixture(t)
	ctx := context.Background()
	if _, err := f.store.ReadNativeLiveRestoreExecution(ctx, nil, claim.Attempt().OperationID); err == nil {
		t.Fatal("journal syntax minted execution without original stop")
	}
	if _, err := f.store.ReadNativeLiveRestoreExecution(ctx, &NativeStoppedPreparation{}, claim.Attempt().OperationID); err == nil {
		t.Fatal("zero stop minted execution")
	}
	if err := f.store.CheckNativeLiveRestoreExecution(ctx, &NativeLiveRestoreExecution{}); err == nil {
		t.Fatal("zero execution passed")
	}
	if _, err := json.Marshal(NativeLiveRestoreExecution{}); err == nil {
		t.Fatal("execution capability serialized")
	}
}
