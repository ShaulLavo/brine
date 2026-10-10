package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
)

// CredentialReceipt reads reference-only delivery metadata. Version zero selects
// the newest delivered version; startup selects the exact committed version.
func (s *Store) CredentialReceipt(ctx context.Context, binding data.ReplicaBindingID, version uint64) (CredentialRecord, error) {
	if !data.ValidID(string(binding)) {
		return CredentialRecord{}, ErrInvalid
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT canonical FROM data_credential_records WHERE binding_id=? AND kind='receipt' AND (?=0 OR json_extract(canonical,'$.version')=?) ORDER BY json_extract(canonical,'$.version') DESC LIMIT 1", binding, version, version).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return CredentialRecord{}, ErrNotFound
	}
	if err != nil {
		return CredentialRecord{}, err
	}
	var record CredentialRecord
	if len(raw) > 8192 || json.Unmarshal(raw, &record) != nil || record.Kind != "receipt" || record.BindingID != binding || record.Version == 0 || record.ReceivedAt.IsZero() || (version != 0 && record.Version != version) {
		return CredentialRecord{}, &IntegrityError{}
	}
	return record, nil
}

// CredentialReceiptForReference is the reference-only expiry projection used
// after the secure file reader has validated the exact immutable version path.
func (s *Store) CredentialReceiptForReference(ctx context.Context, ref string, version uint64) (CredentialRecord, error) {
	if ref == "" || version == 0 {
		return CredentialRecord{}, ErrInvalid
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT canonical FROM data_credential_records WHERE kind='receipt' AND json_extract(canonical,'$.credential_ref')=? AND json_extract(canonical,'$.version')=? LIMIT 1", ref, version).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return CredentialRecord{}, ErrNotFound
	}
	if err != nil {
		return CredentialRecord{}, err
	}
	var record CredentialRecord
	if len(raw) > 8192 || json.Unmarshal(raw, &record) != nil || record.CredentialRef != ref || record.Version != version || record.Kind != "receipt" || record.ReceivedAt.IsZero() {
		return CredentialRecord{}, &IntegrityError{}
	}
	return record, nil
}
