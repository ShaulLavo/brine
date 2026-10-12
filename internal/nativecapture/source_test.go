package nativecapture

import (
	"encoding/json"
	"strings"
	"testing"
)

func fixtureEmptyRecord() EmptyRecord {
	return EmptyRecord{
		Format: Format, Scope: EmptyScope{BindingID: fixtureScope().BindingID, EpochID: fixtureScope().EpochID},
		GenerationID: strings.Repeat("a", 32), ConfigurationSHA256: strings.Repeat("b", 64),
		PublisherReference: "fixture-publisher", SigningRecovery: fixtureCheckpoint().SigningRecovery,
		Members: []CheckpointMember{{Path: "config.toml", Bytes: 32, SHA256: strings.Repeat("b", 64)}},
	}
}

func TestSourceCodecRoundTripAndOwnership(t *testing.T) {
	record := fixtureEmptyRecord()
	empty, err := UnpublishedSource(record, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	record.Members[0].SHA256 = strings.Repeat("f", 64)
	owned, ok := empty.Empty()
	if !ok || owned.Members[0].SHA256 != strings.Repeat("b", 64) {
		t.Fatal("unpublished source aliases caller")
	}
	owned.Members[0].Path = "changed"
	again, _ := empty.Empty()
	if again.Members[0].Path != "config.toml" {
		t.Fatal("unpublished getter aliases source")
	}
	checkpoint, err := NewCheckpoint(fixtureCheckpoint(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	selection, err := Select(fixtureInventory(t), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	initialized, err := InitializedSource(checkpoint, selection, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []Source{empty, initialized} {
		checkSourceRoundTrip(t, source)
	}
}

func checkSourceRoundTrip(t *testing.T, source Source) {
	t.Helper()
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSource(raw, DefaultLimits())
	if err != nil || decoded.Kind() != source.Kind() || decoded.SHA256() != source.SHA256() {
		t.Fatal("source round trip changed record", err)
	}
	if source.Kind() != "initialized" {
		return
	}
	checkpoint, selection, ok := decoded.Initialized()
	_, original, _ := source.Initialized()
	offset, present := selection.SnapshotOffset()
	oldOffset, oldPresent := original.SnapshotOffset()
	if !ok || checkpoint.SHA256() == "" || selection.Kind() != original.Kind() || offset != oldOffset || present != oldPresent {
		t.Fatal("initialized source lost checkpoint/selection")
	}
}

func TestSourceCodecRejectsCrossedArmsAndNulls(t *testing.T) {
	empty, err := UnpublishedSource(fixtureEmptyRecord(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	valid := string(raw)
	for _, invalid := range []string{
		`null`,
		strings.Replace(valid, `"kind":"unpublished"`, `"kind":"other"`, 1),
		strings.Replace(valid, `"kind":"unpublished"`, `"kind":null`, 1),
		strings.Replace(valid, `"kind":"unpublished"`, `"kind":"unpublished","kind":"unpublished"`, 1),
		strings.Replace(valid, `"empty":`, `"members":[],"empty":`, 1),
		strings.Replace(valid, `"empty":`, `"checkpoint":{},"empty":`, 1),
		strings.Replace(valid, `"scope":{`, `"scope":{"replica_id":1,`, 1),
		strings.Replace(valid, `"version":1`, `"version":null`, 1),
		strings.Replace(valid, `"version":1`, `"version":1,"unknown":0`, 1),
		strings.Replace(valid, `"bytes":32`, `"bytes":null`, 1),
		strings.Replace(valid, `"bytes":32`, `"Bytes":32`, 1),
		strings.Replace(valid, `"bytes":32`, `"unknown":0,"bytes":32`, 1),
		strings.Replace(valid, `"generation_id":`+`"`+strings.Repeat("a", 32)+`",`, ``, 1),
		valid + `{}`,
	} {
		if _, err := DecodeSource([]byte(invalid), DefaultLimits()); err == nil {
			t.Fatal("invalid source arm admitted", invalid)
		}
	}
}

func TestInitializedSourceRefusesCrossedGenerationAndDroppedMembers(t *testing.T) {
	checkpoint, err := NewCheckpoint(fixtureCheckpoint(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	selection, err := Select(fixtureInventory(t), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	source, err := InitializedSource(checkpoint, selection, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	valid := string(raw)
	extra, err := json.Marshal(fixtureMember("replicas/1/snapshots/00000000000000000010.snapshot_dir/00000000000000000010.snapshot_bsatn"))
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		strings.Replace(valid, `"kind":"initialized"`, `"kind":"unpublished"`, 1),
		strings.Replace(valid, `"selection_kind":"snapshot"`, `"selection_kind":"logs"`, 1),
		strings.Replace(valid, `"selection_kind":"snapshot"`, `"selection_kind":null`, 1),
		strings.Replace(valid, `"selection_kind":"snapshot"`, `"selection_kind":"other"`, 1),
		strings.Replace(valid, `"checkpoint":`, `"empty":{},"checkpoint":`, 1),
		strings.Replace(valid, `"capture_id":"`+fixtureScope().CaptureID+`"`, `"capture_id":null`, 1),
		strings.Replace(valid, `"replica_id":1`, `"replica_id":2`, 1),
		strings.Replace(valid, `"members":[{"path":"replicas`, `"members":[`+string(extra)+`,{"path":"replicas`, 1),
	} {
		if _, err := DecodeSource([]byte(invalid), DefaultLimits()); err == nil {
			t.Fatal("crossed or unselected source admitted")
		}
	}
	changed := checkpoint.Record()
	changed.Scope.EpochID = strings.Repeat("f", 32)
	mixed, err := NewCheckpoint(changed, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitializedSource(mixed, selection, DefaultLimits()); err == nil {
		t.Fatal("checkpoint from another epoch admitted")
	}
}

func TestSourceLimitsCountCheckpointManifestAndNativeMembers(t *testing.T) {
	checkpoint, err := NewCheckpoint(fixtureCheckpoint(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	selection, err := Select(fixtureInventory(t), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.Files = len(checkpoint.Record().Members) + len(selection.Members())
	if _, err := InitializedSource(checkpoint, selection, limits); err == nil {
		t.Fatal("source manifest member escaped file quota")
	}
	limits = DefaultLimits()
	limits.Bytes, limits.FileBytes = 1024, 1024
	if _, err := InitializedSource(checkpoint, selection, limits); err == nil {
		t.Fatal("source encoded bytes escaped byte quota")
	}
}
