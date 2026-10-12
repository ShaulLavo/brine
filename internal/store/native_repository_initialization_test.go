package store

import (
	"context"
	"testing"

	"github.com/ShaulLavo/brine/internal/restic"
)

func TestNativeIncomingInitializationRequiresActualLeaseBeforeStorage(t *testing.T) {
	state := &Store{}
	if err := state.BeginNativeIncomingInitialization(context.Background(), nil); err == nil {
		t.Fatal("missing actual lease issued repository initialization")
	}
	if _, err := state.RecordNativeIncomingInitialized(context.Background(), nil, restic.InitializedRepository{}); err == nil {
		t.Fatal("missing actual lease/readback stored repository identity")
	}
	if _, err := state.ReadNativeIncomingInitialized(context.Background(), nil); err == nil {
		t.Fatal("missing actual lease read repository identity")
	}
	var missing *NativeIncomingRepository
	if _, err := missing.Metadata(context.Background()); err == nil {
		t.Fatal("missing actual lease disclosed creation metadata")
	}
	if missing.AdmitRepository(context.Background(), restic.Scope{}, restic.CreateRepository, "") == nil {
		t.Fatal("missing actual lease admitted creation")
	}
}

func TestNativeIncomingInitializationJournalNeverReissuesCreation(t *testing.T) {
	if !validNativeRepositoryInitStage(nil, "init_issued") || validNativeRepositoryInitStage(nil, "initialized") || validNativeRepositoryInitStage(nil, "init_unknown") {
		t.Fatal("initialization journal allowed missing original intent")
	}
	issued := []nativeRepositoryInitEvent{{Stage: "init_issued"}}
	if validNativeRepositoryInitStage(issued, "init_issued") || !validNativeRepositoryInitStage(issued, "init_unknown") || !validNativeRepositoryInitStage(issued, "initialized") {
		t.Fatal("issued initialization admitted a repeat or lost readback")
	}
	unknown := []nativeRepositoryInitEvent{{Stage: "init_issued"}, {Stage: "init_unknown"}}
	if validNativeRepositoryInitStage(unknown, "init_issued") || validNativeRepositoryInitStage(unknown, "init_unknown") || !validNativeRepositoryInitStage(unknown, "initialized") {
		t.Fatal("unknown initialization admitted retry instead of readback")
	}
	settled := []nativeRepositoryInitEvent{{Stage: "init_issued"}, {Stage: "init_unknown"}, {Stage: "initialized"}}
	if validNativeRepositoryInitStage(settled, "init_issued") || validNativeRepositoryInitStage(settled, "init_unknown") || validNativeRepositoryInitStage(settled, "initialized") {
		t.Fatal("immutable initialized result reopened effects")
	}
}
