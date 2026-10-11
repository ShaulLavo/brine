package data

import (
	"encoding/json"
	"path/filepath"
)

// SpacetimeDBBinding owns a standalone tree rather than a SQLite file.
type SpacetimeDBBinding struct {
	DatabaseID        DatabaseID       `json:"database_id"`
	Name              DatabaseName     `json:"name"`
	IncarnationID     AppIncarnationID `json:"incarnation_id"`
	Root              PersistentRoot   `json:"root"`
	RelativeDirectory string           `json:"relative_directory"`
	BindingID         ReplicaBindingID `json:"binding_id"`
}

type ArchiveBinding struct {
	BindingID    ReplicaBindingID `json:"binding_id"`
	DatabaseID   DatabaseID       `json:"database_id"`
	Destination  Destination      `json:"destination"`
	EpochID      ReplicaEpochID   `json:"epoch_id"`
	RemotePrefix string           `json:"remote_prefix"`
}

func (b SpacetimeDBBinding) Validate() error {
	relative, err := RelativeDirectory(b.IncarnationID, b.DatabaseID)
	if err != nil || b.RelativeDirectory != relative || !ValidID(string(b.BindingID)) || !namePattern.MatchString(string(b.Name)) || !ValidRoot(string(b.Root)) {
		return ErrInvalid
	}
	return nil
}

func (b SpacetimeDBBinding) SourcePath() string {
	return filepath.Join(string(b.Root), b.RelativeDirectory)
}

func (b ArchiveBinding) Validate(database SpacetimeDBBinding) error {
	prefix, err := RemotePrefix(b.Destination.BasePrefix, database.IncarnationID, database.DatabaseID, b.EpochID)
	if database.Validate() != nil || b.Destination.Validate() != nil || err != nil || prefix != b.RemotePrefix || b.DatabaseID != database.DatabaseID || b.BindingID != database.BindingID {
		return ErrInvalid
	}
	return nil
}

func (b *SpacetimeDBBinding) UnmarshalJSON(raw []byte) error {
	if err := engineRecord(raw, []string{"database_id", "name", "incarnation_id", "root", "relative_directory", "binding_id"}, nil); err != nil {
		return err
	}
	type record SpacetimeDBBinding
	var value record
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	decoded := SpacetimeDBBinding(value)
	if err := decoded.Validate(); err != nil {
		return err
	}
	*b = decoded
	return nil
}

func (b *ArchiveBinding) UnmarshalJSON(raw []byte) error {
	fields, err := engineRecordFields(raw, []string{"binding_id", "database_id", "destination", "epoch_id", "remote_prefix"}, nil)
	if err != nil {
		return err
	}
	if err = engineRecord(fields["destination"], []string{"reference", "endpoint", "region", "bucket", "base_prefix", "path_style", "credential_ref"}, nil); err != nil {
		return err
	}
	type record ArchiveBinding
	var value record
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*b = ArchiveBinding(value)
	return nil
}
