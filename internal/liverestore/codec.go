package liverestore

import (
	"bytes"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

type planRecord struct {
	Format string   `json:"format"`
	Spec   PlanSpec `json:"spec"`
	Loss   Loss     `json:"loss"`
}

func (p Plan) MarshalJSON() ([]byte, error) {
	if !planID(p.id) || len(p.encoded) == 0 {
		return nil, ErrRecord
	}
	return p.Bytes(), nil
}

// DecodePlan validates the canonical review record, never caller authority.
func DecodePlan(raw []byte) (Plan, error) {
	if len(raw) == 0 || len(raw) > RecordLimit {
		return Plan{}, ErrRecord
	}
	fields, err := strictjson.Object(raw, "format", "spec", "loss")
	if err != nil || validateSpecJSON(fields["spec"]) != nil {
		return Plan{}, ErrRecord
	}
	if _, err = strictjson.Object(fields["loss"], "kind", "selected_offset", "current_offset", "committed_offsets_lost", "unknown_reason"); err != nil {
		return Plan{}, ErrRecord
	}
	var record planRecord
	if json.Unmarshal(raw, &record) != nil || record.Format != Format {
		return Plan{}, ErrRecord
	}
	p, err := NewPlan(record.Spec)
	if err != nil || p.loss != record.Loss || !bytes.Equal(p.encoded, raw) {
		return Plan{}, ErrRecord
	}
	return p, nil
}

func validateSpecJSON(raw json.RawMessage) error {
	fields, err := strictjson.Object(raw, "requester", "facts", "safety", "selected", "recovery", "issued_at", "expires_at")
	if err != nil {
		return ErrRecord
	}
	facts, err := strictjson.Object(fields["facts"], "app", "incarnation_id", "database_id", "head", "generation", "generation_id", "policy_sha256", "target_sha256", "desired_sha256")
	if err != nil {
		return ErrRecord
	}
	if _, err = strictjson.Object(facts["head"], "binding_id", "epoch_id", "version", "runtime_sha256"); err != nil {
		return ErrRecord
	}
	if _, err = strictjson.Object(fields["safety"], "point_id", "point_sha256", "fence_id", "fence_owner_op_id", "source_seal_sha256", "source_witness_sha256", "stop_proof_sha256", "checkpoint_sha256", "committed_offset", "prepared_at", "expires_at"); err != nil {
		return ErrRecord
	}
	if _, err = strictjson.Object(fields["selected"], "incarnation_id", "database_id", "binding_id", "point_id", "point_sha256", "epoch_id", "generation_id", "capture_id", "repository_id", "repository_snapshot", "manifest_sha256", "restore_receipt_sha256", "checkpoint_sha256", "state_witness_sha256", "native_database", "native_publisher", "program_wire_hash", "artifact_wasm_sha256", "schema_sha256", "material_sha256", "dependencies_sha256", "committed_offset", "recorded_at", "retain_until"); err != nil {
		return ErrRecord
	}
	if _, err = strictjson.Object(fields["recovery"], "release_id", "code_sha256", "compatibility_sha256", "program_wire_hash", "schema_sha256"); err != nil {
		return ErrRecord
	}
	return nil
}
