package datapublish

import (
	"bytes"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

func objectValues(raw json.RawMessage, fields ...string) (map[string]json.RawMessage, error) {
	values, err := strictjson.Object(raw, fields...)
	if err != nil {
		return nil, ErrRecord
	}
	for _, value := range values {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, ErrRecord
		}
	}
	return values, nil
}

func validatePlanObjects(fields map[string]json.RawMessage, kind SourceKind) error {
	for _, raw := range fields {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return ErrRecord
		}
	}
	facts, err := objectValues(fields["facts"], "app", "incarnation_id", "database_id", "binding_id", "epoch_id", "generation", "target_sha256", "policy_sha256", "desired_sha256", "config_sha256", "secret_versions")
	if err != nil {
		return err
	}
	secrets, err := objectValues(facts["secret_versions"], "publisher", "signing_public", "signing_private", "auth_policy", "recovery_password")
	if err != nil {
		return err
	}
	for _, raw := range secrets {
		if _, err = objectValues(raw, "reference", "version", "sha256", "expires_at"); err != nil {
			return err
		}
	}
	candidate, err := objectValues(fields["candidate"], "wasm_sha256", "program_wire_hash", "schema")
	if err != nil {
		return err
	}
	if _, err = objectValues(candidate["schema"], "version", "sha256"); err != nil {
		return err
	}
	if _, err = objectValues(fields["review"], "guest_code_sha256", "recovery"); err != nil {
		return err
	}
	if kind == CapturedSource {
		if _, err = objectValues(fields["preflight"], "response_sha256", "token_sha256", "private_token_ref"); err != nil {
			return err
		}
	}
	return validateSourceObjects(fields["source"], kind)
}

func validateSourceObjects(raw json.RawMessage, kind SourceKind) error {
	switch kind {
	case CreateSource:
		fields, err := objectValues(raw, "kind", "empty")
		if err != nil {
			return err
		}
		_, err = objectValues(fields["empty"], "allocation_id", "point_id", "fence_id", "preparation_op_id", "restore_receipt_sha256", "bundle_manifest_sha256", "source_seal_sha256", "config_sha256", "signing_recovery_sha256")
		return err
	case CapturedSource:
		fields, err := objectValues(raw, "kind", "captured")
		if err != nil {
			return err
		}
		point, err := objectValues(fields["captured"], "point", "schema", "coverage_contract")
		if err != nil {
			return err
		}
		if _, err = objectValues(point["schema"], "version", "sha256"); err != nil {
			return err
		}
		_, err = objectValues(point["point"], "point_id", "fence_id", "preparation_op_id", "restore_receipt_sha256", "bundle_manifest_sha256", "source_seal_sha256", "state_witness_sha256", "stop_proof_sha256", "checkpoint_sha256", "native_database", "native_publisher", "program_wire_hash", "wasm_sha256", "names")
		return err
	default:
		return ErrRecord
	}
}
