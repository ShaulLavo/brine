package nativecapture

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func fixtureScope() Scope {
	return Scope{BindingID: strings.Repeat("a", 32), EpochID: strings.Repeat("b", 32), CaptureID: strings.Repeat("c", 32), ReplicaID: 1}
}

func fixtureMember(path string) Member {
	return Member{Path: path, Bytes: 128, ExpandedBytes: 128, SHA256: strings.Repeat("d", 64), Device: 1, Inode: 2, MountID: 3, UID: 1000, Mode: 0600, Links: 1}
}

func fixtureInventory(t *testing.T) Inventory {
	t.Helper()
	members := []Member{
		fixtureMember("replicas/1/clog/00000000000000000000.stdb.log"),
		fixtureMember("replicas/1/clog/00000000000000000020.stdb.log"),
		fixtureMember("replicas/1/clog/00000000000000000040.stdb.log"),
		fixtureMember("replicas/1/snapshots/00000000000000000010.snapshot_dir/00000000000000000010.snapshot_bsatn"),
		fixtureMember("replicas/1/snapshots/00000000000000000030.snapshot_dir/00000000000000000030.snapshot_bsatn"),
	}
	for i := range members {
		members[i].Inode = uint64(i + 2)
	}
	inventory, err := NewInventory(fixtureScope(), members, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return inventory
}

func TestSelectionDerivesPinnedPathsAndOwnsInput(t *testing.T) {
	inventory := fixtureInventory(t)
	selection, err := Select(inventory, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	offset, snapshot := selection.SnapshotOffset()
	if !snapshot || selection.Kind() != "snapshot" || offset != 30 || len(selection.Members()) != 3 {
		t.Fatal("wrong snapshot/suffix selected")
	}
	members := selection.Members()
	if members[1].Path != "replicas/1/clog/00000000000000000020.stdb.log" || members[2].Path != "replicas/1/clog/00000000000000000040.stdb.log" {
		t.Fatal("containing suffix missing")
	}
	members[0].SHA256 = strings.Repeat("f", 64)
	if selection.Members()[0].SHA256 == members[0].SHA256 {
		t.Fatal("caller changed immutable selection")
	}
}

func TestSelectionNamesDoNotInferCommittedHead(t *testing.T) {
	for _, test := range []struct {
		name     string
		logs     []uint64
		snapshot uint64
		wantLogs int
	}{
		{name: "snapshot inside only segment", logs: []uint64{0}, snapshot: 10, wantLogs: 1},
		{name: "adjacent retained segment", logs: []uint64{11}, snapshot: 10, wantLogs: 1},
		{name: "adjacent excludes older segment", logs: []uint64{0, 11}, snapshot: 10, wantLogs: 1},
		{name: "snapshot only", snapshot: 10},
		{name: "maximum snapshot", logs: []uint64{0}, snapshot: ^uint64(0), wantLogs: 1},
	} {
		t.Run(test.name, func(t *testing.T) { checkStructuralSelection(t, test.logs, test.snapshot, test.wantLogs) })
	}
}

func checkStructuralSelection(t *testing.T, logs []uint64, snapshot uint64, wantLogs int) {
	t.Helper()
	members := []Member{fixtureMember(fmt.Sprintf("replicas/1/snapshots/%020d.snapshot_dir/%020d.snapshot_bsatn", snapshot, snapshot))}
	for _, offset := range logs {
		members = append(members, fixtureMember(fmt.Sprintf("replicas/1/clog/%020d.stdb.log", offset)))
	}
	inventory, err := NewInventory(fixtureScope(), members, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	selection, err := Select(inventory, DefaultLimits())
	offset, presentSnapshot := selection.SnapshotOffset()
	if err != nil || !presentSnapshot || offset != snapshot || len(selection.Members()) != wantLogs+1 {
		t.Fatal("structural selection unavailable", selection, err)
	}
	tail, present := selection.TailMember()
	if present != (wantLogs > 0) {
		t.Fatal("tail presence disagrees with selected filenames")
	}
	if present && tail.Path != fmt.Sprintf("replicas/1/clog/%020d.stdb.log", logs[len(logs)-1]) {
		t.Fatal("wrong structural tail member")
	}
}

func TestSelectionRefusesGapAndSnapshotObjectsWithoutManifest(t *testing.T) {
	for _, members := range [][]Member{
		{fixtureMember("replicas/1/clog/00000000000000000001.stdb.log")},
		{fixtureMember("replicas/1/snapshots/00000000000000000010.snapshot_dir/objects/aa/" + strings.Repeat("b", 62))},
		{fixtureMember("replicas/1/snapshots/00000000000000000010.snapshot_dir/00000000000000000010.snapshot_bsatn"), fixtureMember("replicas/1/clog/00000000000000000012.stdb.log")},
	} {
		inventory, err := NewInventory(fixtureScope(), members, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Select(inventory, DefaultLimits()); !errors.Is(err, ErrSelection) {
			t.Fatal("missing structural history admitted", err)
		}
	}
}

func TestLogOnlySelectionStartsAtNativeRoot(t *testing.T) {
	members := []Member{
		fixtureMember("replicas/1/clog/00000000000000000000.stdb.log"),
		fixtureMember("replicas/1/clog/00000000000000000020.stdb.log"),
	}
	inventory, err := NewInventory(fixtureScope(), members, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	selection, err := Select(inventory, DefaultLimits())
	if err != nil || selection.Kind() != "logs" || len(selection.Members()) != 2 {
		t.Fatal("rooted log-only selection unavailable", err)
	}
	if _, present := selection.SnapshotOffset(); present {
		t.Fatal("log-only selection invented snapshot provenance")
	}
	checkpoint, err := NewCheckpoint(fixtureCheckpoint(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	source, err := InitializedSource(checkpoint, selection, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	checkSourceRoundTrip(t, source)
}

func TestInventoryRejectsUnknownPathsAndBounds(t *testing.T) {
	for _, path := range []string{
		"../clog/00000000000000000000.stdb.log",
		"replicas/2/clog/00000000000000000000.stdb.log",
		"replicas/1/clog/0.stdb.log",
		"replicas/1/snapshots/00000000000000000010.invalid_snapshot/00000000000000000010.snapshot_bsatn",
		"replicas/1/snapshots/00000000000000000010.snapshot_dir/00000000000000000011.snapshot_bsatn",
		"replicas/1/snapshots/00000000000000000010.snapshot_dir/objects/a/" + strings.Repeat("b", 62),
		"replicas/1/snapshots/00000000000000000010.snapshot_dir/objects/aa/" + strings.Repeat("B", 62),
		"replicas/1/snapshots/00000000000000000010.snapshot_dir/objects/aa/bb/" + strings.Repeat("c", 62),
	} {
		if _, err := NewInventory(fixtureScope(), []Member{fixtureMember(path)}, DefaultLimits()); err == nil {
			t.Fatalf("unsafe member admitted: %s", path)
		}
	}
	limits := DefaultLimits()
	limits.Bytes = 1
	limits.FileBytes = 1
	if _, err := NewInventory(fixtureScope(), fixtureInventory(t).Members(), limits); !errors.Is(err, ErrLimit) {
		t.Fatal("quota overflow admitted", err)
	}
}

func TestDriftRefusesRewriteRemovalAndRotation(t *testing.T) {
	original := fixtureInventory(t)
	for _, change := range []string{"rewrite", "remove", "rotation"} {
		members := original.Members()
		switch change {
		case "rewrite":
			members[0].SHA256 = strings.Repeat("e", 64)
		case "remove":
			members = members[1:]
		case "rotation":
			member := fixtureMember(fmt.Sprintf("replicas/1/clog/%020d.stdb.log", 60))
			member.Inode = 20
			members = append(members, member)
		}
		after, err := NewInventory(fixtureScope(), members, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckUnchanged(original, after); !errors.Is(err, ErrDrift) {
			t.Fatal("changed native input accepted", change, err)
		}
	}
}
