package nativecapture

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

func closedObject(raw []byte, fields ...string) (map[string]json.RawMessage, error) {
	values, err := strictjson.Object(raw, fields...)
	if err != nil {
		return nil, ErrInvalid
	}
	for _, value := range values {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, ErrInvalid
		}
	}
	return values, nil
}

func DecodeCheckpoint(raw []byte, limits Limits) (Checkpoint, error) {
	if len(raw) > 1<<20 {
		return Checkpoint{}, ErrLimit
	}
	fields, err := closedObject(raw, "format", "scope", "generation_id", "database_identity", "publisher_identity", "names", "runtime_module_hash", "artifact_sha256", "schema_version", "schema_sha256", "configuration_sha256", "signing_recovery", "members")
	if err != nil {
		return Checkpoint{}, err
	}
	if _, err := closedObject(fields["scope"], "binding_id", "epoch_id", "replica_id"); err != nil {
		return Checkpoint{}, err
	}
	if _, err := closedObject(fields["signing_recovery"], "kind", "reference", "version", "sha256", "member"); err != nil {
		return Checkpoint{}, err
	}
	values, err := strictjson.Value[[]json.RawMessage](fields["members"])
	if err != nil {
		return Checkpoint{}, ErrInvalid
	}
	if len(values) > limits.Files {
		return Checkpoint{}, ErrLimit
	}
	for _, value := range values {
		if _, err := closedObject(value, "path", "bytes", "sha256"); err != nil {
			return Checkpoint{}, err
		}
	}
	record, err := strictjson.Value[CheckpointRecord](json.RawMessage(raw))
	if err != nil {
		return Checkpoint{}, ErrInvalid
	}
	return NewCheckpoint(record, limits)
}

type inventoryRecord struct {
	Format  string   `json:"format"`
	Scope   Scope    `json:"scope"`
	Members []Member `json:"members"`
}

func (i Inventory) MarshalJSON() ([]byte, error) {
	if !i.scope.valid() || len(i.members) == 0 {
		return nil, ErrInvalid
	}
	return json.Marshal(inventoryRecord{Format: Format, Scope: i.scope, Members: i.members})
}

func DecodeInventory(raw []byte, limits Limits) (Inventory, error) {
	if len(raw) > 2<<20 {
		return Inventory{}, ErrLimit
	}
	fields, err := closedObject(raw, "format", "scope", "members")
	if err != nil {
		return Inventory{}, err
	}
	if _, err := closedObject(fields["scope"], "binding_id", "epoch_id", "capture_id", "replica_id"); err != nil {
		return Inventory{}, err
	}
	values, err := strictjson.Value[[]json.RawMessage](fields["members"])
	if err != nil {
		return Inventory{}, ErrInvalid
	}
	if len(values) > limits.Files {
		return Inventory{}, ErrLimit
	}
	for _, value := range values {
		if _, err := closedObject(value, "path", "bytes", "expanded_bytes", "sha256", "device", "inode", "mount_id", "uid", "mode", "links"); err != nil {
			return Inventory{}, err
		}
	}
	record, err := strictjson.Value[inventoryRecord](json.RawMessage(raw))
	if err != nil || record.Format != Format {
		return Inventory{}, ErrInvalid
	}
	return NewInventory(record.Scope, record.Members, limits)
}

func DecodeSource(raw []byte, limits Limits) (Source, error) {
	if len(raw) > 2<<20 {
		return Source{}, ErrLimit
	}
	var kind struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &kind); err != nil {
		return Source{}, ErrInvalid
	}
	if kind.Kind == "unpublished" {
		return decodeEmptySource(raw, limits)
	}
	if kind.Kind != "initialized" {
		return Source{}, ErrInvalid
	}
	fields, err := closedObject(raw, "kind", "selection_kind", "checkpoint", "scope", "members")
	if err != nil {
		return Source{}, err
	}
	checkpoint, err := DecodeCheckpoint(fields["checkpoint"], limits)
	if err != nil {
		return Source{}, err
	}
	inventoryRaw, err := json.Marshal(struct {
		Format  string          `json:"format"`
		Scope   json.RawMessage `json:"scope"`
		Members json.RawMessage `json:"members"`
	}{Format, fields["scope"], fields["members"]})
	if err != nil {
		return Source{}, ErrInvalid
	}
	inventory, err := DecodeInventory(inventoryRaw, limits)
	if err != nil {
		return Source{}, err
	}
	selection, err := Select(inventory, limits)
	if err != nil {
		return Source{}, err
	}
	kindValue, err := strictjson.Value[string](fields["selection_kind"])
	if err != nil || kindValue != selection.kind {
		return Source{}, ErrInvalid
	}
	selected := selection.Members()
	slices.SortFunc(selected, func(a, b Member) int { return comparePath(a.Path, b.Path) })
	if !slices.Equal(selected, inventory.members) {
		return Source{}, ErrInvalid
	}
	return InitializedSource(checkpoint, selection, limits)
}

func decodeEmptySource(raw []byte, limits Limits) (Source, error) {
	fields, err := closedObject(raw, "kind", "empty")
	if err != nil {
		return Source{}, err
	}
	empty, err := closedObject(fields["empty"], "format", "scope", "generation_id", "configuration_sha256", "publisher_reference", "signing_recovery", "members")
	if err != nil {
		return Source{}, err
	}
	if _, err := closedObject(empty["scope"], "binding_id", "epoch_id"); err != nil {
		return Source{}, err
	}
	if _, err := closedObject(empty["signing_recovery"], "kind", "reference", "version", "sha256", "member"); err != nil {
		return Source{}, err
	}
	values, err := strictjson.Value[[]json.RawMessage](empty["members"])
	if err != nil {
		return Source{}, ErrInvalid
	}
	if len(values) > limits.Files {
		return Source{}, ErrLimit
	}
	for _, value := range values {
		if _, err := closedObject(value, "path", "bytes", "sha256"); err != nil {
			return Source{}, err
		}
	}
	record, err := strictjson.Value[EmptyRecord](fields["empty"])
	if err != nil {
		return Source{}, ErrInvalid
	}
	return UnpublishedSource(record, limits)
}
