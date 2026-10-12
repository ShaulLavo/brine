package archive

import (
	"encoding/json"
)

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
	return exactArchiveObject(raw, []string{"transition_id", "stage", "seal"})
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
