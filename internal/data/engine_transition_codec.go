package data

import "encoding/json"

func checkEngineEvidenceJSON(raw []byte) error {
	return engineRecord(raw, []string{"kind", "id", "sha256", "database_id", "binding_id", "epoch_id"}, nil)
}

func (r *EngineEvidenceRef) UnmarshalJSON(raw []byte) error {
	if checkEngineEvidenceJSON(raw) != nil {
		return ErrInvalid
	}
	type record EngineEvidenceRef
	var decoded record
	if json.Unmarshal(raw, &decoded) != nil || !knownEngineEvidence(decoded.Kind) {
		return ErrInvalid
	}
	value := EngineEvidenceRef(decoded)
	if !value.validFor(value.Kind) {
		return ErrInvalid
	}
	*r = value
	return nil
}

func knownEngineEvidence(kind EngineEvidenceKind) bool {
	switch kind {
	case EngineAllocationEvidence, EnginePreparedPointEvidence, EngineSourceWitnessEvidence, EngineTargetEvidence,
		EngineCheckpointEvidence, EngineMaterialEvidence, EngineRecoveryEvidence, EngineDependenciesEvidence,
		EnginePublicationEvidence, EngineRestorePointEvidence:
		return true
	default:
		return false
	}
}

func (d *EngineDirectoryIdentity) UnmarshalJSON(raw []byte) error {
	if engineRecord(raw, []string{"device", "inode"}, nil) != nil {
		return ErrInvalid
	}
	type record EngineDirectoryIdentity
	var value record
	if json.Unmarshal(raw, &value) != nil || !EngineDirectoryIdentity(value).valid() {
		return ErrInvalid
	}
	*d = EngineDirectoryIdentity(value)
	return nil
}

func (s *EngineTreeSeal) UnmarshalJSON(raw []byte) error {
	if engineRecord(raw, []string{"root", "parent", "directory", "contents_sha256"}, nil) != nil {
		return ErrInvalid
	}
	type record EngineTreeSeal
	var value record
	if json.Unmarshal(raw, &value) != nil || EngineTreeSeal(value).Validate() != nil {
		return ErrInvalid
	}
	*s = EngineTreeSeal(value)
	return nil
}

func (s *EngineTransitionScope) UnmarshalJSON(raw []byte) error {
	fields := []string{"id", "apply_operation_id", "plan_id", "requester", "policy_hash", "target_hash", "desired_hash", "generation", "incarnation_id", "database_id", "binding_id", "expires_at"}
	if engineRecord(raw, fields, nil) != nil {
		return ErrInvalid
	}
	type record EngineTransitionScope
	var value record
	if json.Unmarshal(raw, &value) != nil || EngineTransitionScope(value).Validate() != nil {
		return ErrInvalid
	}
	*s = EngineTransitionScope(value)
	return nil
}

func (c *EngineTransitionCommon) UnmarshalJSON(raw []byte) error {
	fields := []string{"engine", "scope", "base", "new_epoch_id", "fence_id", "fence_owner_operation_id", "prepared_point", "source_witness", "old_tree", "target_tree", "preserved_parent", "target_verification", "target_runtime_sha256", "target_checkpoint", "target_material", "recovery_release", "retained_dependencies"}
	if engineRecord(raw, fields, nil) != nil {
		return ErrInvalid
	}
	type record EngineTransitionCommon
	var value record
	if json.Unmarshal(raw, &value) != nil || EngineTransitionCommon(value).validate() != nil {
		return ErrInvalid
	}
	*c = EngineTransitionCommon(value)
	return nil
}

func DecodeEngineTransition(raw []byte) (EngineTransition, error) {
	if len(raw) == 0 || len(raw) > MaxEngineTransitionBytes {
		return EngineTransition{}, ErrInvalid
	}
	fields, err := engineRecordFields(raw, []string{"format", "purpose", "common"}, []string{"publish", "restore"})
	if err != nil || len(fields) != 4 {
		return EngineTransition{}, ErrInvalid
	}
	var format, purpose string
	if json.Unmarshal(fields["format"], &format) != nil || format != EngineTransitionFormat ||
		json.Unmarshal(fields["purpose"], &purpose) != nil || fields[purpose] == nil {
		return EngineTransition{}, ErrInvalid
	}
	var common EngineTransitionCommon
	if json.Unmarshal(fields["common"], &common) != nil {
		return EngineTransition{}, ErrInvalid
	}
	return decodeEngineTransitionTarget(common, purpose, fields[purpose])
}

func decodeEngineTransitionTarget(common EngineTransitionCommon, purpose string, raw []byte) (EngineTransition, error) {
	switch purpose {
	case "publish":
		if engineRecord(raw, []string{"candidate_sha256", "receipt"}, nil) != nil {
			return EngineTransition{}, ErrInvalid
		}
		var target EnginePublicationTarget
		if json.Unmarshal(raw, &target) != nil {
			return EngineTransition{}, ErrInvalid
		}
		return NewEnginePublicationTransition(common, target)
	case "restore":
		if engineRecord(raw, []string{"selected_point"}, nil) != nil {
			return EngineTransition{}, ErrInvalid
		}
		var target EngineRestoreTarget
		if json.Unmarshal(raw, &target) != nil {
			return EngineTransition{}, ErrInvalid
		}
		return NewEngineRestoreTransition(common, target)
	default:
		return EngineTransition{}, ErrInvalid
	}
}

func (t *EngineTransition) UnmarshalJSON(raw []byte) error {
	decoded, err := DecodeEngineTransition(raw)
	if err != nil {
		return err
	}
	*t = decoded
	return nil
}
