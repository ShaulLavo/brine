package archive

import (
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type nativeArchiveTreeRecord struct {
	TransitionID string                     `json:"transition_id"`
	Stage        data.EngineTransitionStage `json:"stage"`
	Seal         *data.EngineTreeSeal       `json:"seal"`
	Optional     bool                       `json:"optional,omitempty"`
}

func (t NativeTransitionTree) MarshalJSON() ([]byte, error) {
	record := nativeArchiveTreeRecord{TransitionID: t.TransitionID, Stage: t.Stage, Optional: t.Optional}
	if !t.Absent() {
		record.Seal = &t.Seal
	}
	return json.Marshal(record)
}

func (t *NativeTransitionTree) UnmarshalJSON(raw []byte) error {
	if err := nativeArchiveTransitionRecord(raw); err != nil {
		return err
	}
	var record nativeArchiveTreeRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return err
	}
	if record.Optional && record.Stage != data.EngineTransitionAbandoned || record.Seal == nil && !record.Optional {
		return ErrRefused
	}
	value := NativeTransitionTree{TransitionID: record.TransitionID, Stage: record.Stage, Optional: record.Optional}
	if record.Seal != nil {
		value.Seal = *record.Seal
	}
	*t = value
	return nil
}

func strictNativeArchiveHead(raw []byte) error {
	return exactArchiveObject(raw, []string{"binding_id", "epoch_id", "version", "runtime_sha256"})
}

func strictNativeArchiveDatabase(raw []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || strictNativeArchiveHead(fields["head"]) != nil || strictNativeArchiveAllocation(fields["allocation"]) != nil {
		return ErrRefused
	}
	if strictNativeArchiveArray(fields["retention"], nativeArchiveRetentionRecord) != nil {
		return ErrRefused
	}
	return strictNativeArchiveArray(fields["transition_trees"], nativeArchiveTransitionRecord)
}

func strictNativeArchiveArray(raw []byte, record func([]byte) error) error {
	var records []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &records) != nil || records == nil {
		return ErrRefused
	}
	for _, item := range records {
		if record(item) != nil {
			return ErrRefused
		}
	}
	return nil
}

func nativeArchiveRetentionRecord(raw []byte) error {
	return exactArchiveObject(raw, []string{"destination", "endpoint", "bucket", "base_prefix", "verification_id", "verified_at", "freshness_seconds", "no_object_expiration"})
}

func nativeArchiveTransitionRecord(raw []byte) error {
	if err := exactArchiveObject(raw, []string{"transition_id", "stage", "seal"}, "optional"); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if value, present := fields["optional"]; present {
		_, err := strictjson.Value[bool](value)
		return err
	}
	return nil
}

func strictNativeArchiveAllocation(raw []byte) error {
	if exactArchiveObject(raw, []string{"database_id", "incarnation_id", "root_device", "root_inode", "directory_device", "directory_inode", "allocated_at"}, "root_proof", "mapping_proof") != nil {
		return ErrRefused
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ErrRefused
	}
	if proof, exists := fields["root_proof"]; exists && strictNativeArchiveRoot(proof) != nil {
		return ErrRefused
	}
	if proof, exists := fields["mapping_proof"]; exists && strictNativeArchiveMapping(proof) != nil {
		return ErrRefused
	}
	return nil
}

func strictNativeArchiveRoot(raw []byte) error {
	if exactArchiveObject(raw, []string{"root", "device", "inode", "filesystem", "posix_locks", "durable_rename", "free_bytes", "free_inodes", "observed_at"}, "operator_quota") != nil {
		return ErrRefused
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ErrRefused
	}
	quota, exists := fields["operator_quota"]
	if exists {
		return exactArchiveObject(quota, []string{"limit_bytes", "used_bytes", "enforced_by"})
	}
	return nil
}

func strictNativeArchiveMapping(raw []byte) error {
	if exactArchiveObject(raw, []string{"runtime", "runner_uid", "runner_gid", "image", "root", "device", "keep_id", "private_modes", "host_read_write", "container_read_write", "observed_at"}) != nil {
		return ErrRefused
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ErrRefused
	}
	return exactArchiveObject(fields["runtime"], []string{"uid", "gid"})
}
