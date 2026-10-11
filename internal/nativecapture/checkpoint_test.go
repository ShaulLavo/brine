package nativecapture

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

func fixtureCheckpoint() CheckpointRecord {
	return CheckpointRecord{Format: Format, Scope: CheckpointScope{BindingID: fixtureScope().BindingID, EpochID: fixtureScope().EpochID, ReplicaID: 1}, GenerationID: strings.Repeat("1", 32), DatabaseIdentity: strings.Repeat("2", 64), PublisherIdentity: strings.Repeat("3", 64), Names: []string{"fixture"}, RuntimeModuleHash: strings.Repeat("4", 64), ArtifactSHA256: strings.Repeat("5", 64), SchemaVersion: spacetimedb.CanonicalizerVersion, SchemaSHA256: strings.Repeat("6", 64), ConfigurationSHA256: strings.Repeat("7", 64), SigningRecovery: SigningRecovery{Kind: "external_immutable", Reference: "fixture-signing", Version: 1, SHA256: strings.Repeat("8", 64)}, Members: []CheckpointMember{{Path: "config.toml", Bytes: 32, SHA256: strings.Repeat("7", 64)}, {Path: "control-db/db", Bytes: 64, SHA256: strings.Repeat("9", 64)}}}
}

func TestCheckpointCodecOwnsEvidenceAndRefusesMixedGeneration(t *testing.T) {
	record := fixtureCheckpoint()
	checkpoint, err := NewCheckpoint(record, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	record.Names[0] = "changed"
	record.Members[0].SHA256 = strings.Repeat("f", 64)
	if checkpoint.Record().Names[0] != "fixture" {
		t.Fatal("checkpoint retained caller aliases")
	}
	decoded, err := DecodeCheckpoint(checkpoint.Bytes(), DefaultLimits())
	if err != nil || decoded.SHA256() != checkpoint.SHA256() {
		t.Fatal("checkpoint codec failed", err)
	}
	changed := checkpoint.Record()
	changed.GenerationID = strings.Repeat("a", 32)
	after, err := NewCheckpoint(changed, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckGeneration(checkpoint, after); !errors.Is(err, ErrDrift) {
		t.Fatal("mixed checkpoint accepted", err)
	}
	for _, invalid := range []string{
		strings.Replace(string(checkpoint.Bytes()), `"members":`, `"unknown":true,"members":`, 1),
		strings.Replace(string(checkpoint.Bytes()), `"replica_id":1`, `"replica_id":null`, 1),
		strings.Replace(string(checkpoint.Bytes()), `"names":["fixture"]`, `"names":null`, 1),
		strings.Replace(string(checkpoint.Bytes()), `"bytes":32`, `"bytes":32,"bytes":32`, 1),
		strings.Replace(string(checkpoint.Bytes()), `"config.toml"`, `"../config.toml"`, 1),
	} {
		if _, err := DecodeCheckpoint([]byte(invalid), DefaultLimits()); err == nil {
			t.Fatal("invalid checkpoint admitted")
		}
	}
}

func TestInventoryCodecClosedAndEmptyDistinct(t *testing.T) {
	inventory := fixtureInventory(t)
	raw, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeInventory(raw, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	invalid := strings.Replace(string(raw), `"uid":1000`, `"uid":null`, 1)
	if _, err := DecodeInventory([]byte(invalid), DefaultLimits()); err == nil {
		t.Fatal("null member field admitted")
	}
	empty := fixtureEmptyRecord()
	source, err := UnpublishedSource(empty, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"replica_id", "database_identity", "publisher_identity", "runtime_module_hash", "snapshot", "framing"} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatal("empty allocation invented native facts", forbidden)
		}
	}
	if _, _, ok := source.Initialized(); ok {
		t.Fatal("empty source became initialized")
	}
}
