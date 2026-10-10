package target

import (
	"fmt"

	"github.com/ShaulLavo/brine/internal/data"
)

// PersistentDatabase is host-collected decision evidence. No client-supplied
// declaration, inode existence or active service substitutes for these facts.
type PersistentDatabase struct {
	Credentials Observation[data.CredentialEvidence] `json:"credentials"`
	Usage       Observation[data.StorageUsage]       `json:"usage"`
	Definitions []data.SchemaDefinition              `json:"definitions"`
	Database    data.DatabaseBinding                 `json:"database"`
	Root        data.RootEvidence                    `json:"root"`
	Mapping     Observation[data.MappingEvidence]    `json:"mapping"`
	Retention   Observation[data.RetentionEvidence]  `json:"retention"`
	Schema      data.SchemaObservation               `json:"schema"`
	Fenced      bool                                 `json:"fenced"`
}

func validatePersistentData(value []PersistentDatabase) error {
	if value == nil || len(value) > 16 {
		return fmt.Errorf("invalid persistent database evidence")
	}
	ids := map[data.DatabaseID]bool{}
	names := map[data.DatabaseName]bool{}
	for _, fact := range value {
		b := fact.Database
		if !data.ValidID(string(b.DatabaseID)) || !data.ValidID(string(b.IncarnationID)) || ids[b.DatabaseID] || names[b.Name] || b.DatabaseID != fact.Schema.DatabaseID {
			return fmt.Errorf("invalid persistent database identity")
		}
		ids[b.DatabaseID] = true
		names[b.Name] = true
		relative, err := data.RelativeDirectory(b.IncarnationID, b.DatabaseID)
		if err != nil || relative != b.RelativeDirectory {
			return fmt.Errorf("invalid persistent database path")
		}
		if err = observe("credentials", fact.Credentials, false, nil); err != nil {
			return err
		}
		if err = observe("usage", fact.Usage, false, nil); err != nil {
			return err
		}
		if err = observe("mapping", fact.Mapping, false, nil); err != nil {
			return err
		}
		if err = observe("retention", fact.Retention, false, nil); err != nil {
			return err
		}
		switch fact.Schema.State {
		case data.AllocatedEmpty, data.VerifiedEmpty, data.VerifiedSchema:
			if !data.ValidMarker(fact.Schema.Marker) || !data.ValidCatalogHash(fact.Schema.CatalogSHA256) || fact.Schema.UnknownReason != "" {
				return fmt.Errorf("invalid known schema evidence")
			}
		case data.Unknown:
			if fact.Schema.Marker != "" || fact.Schema.CatalogSHA256 != "" {
				return fmt.Errorf("invalid unknown schema evidence")
			}
		default:
			return fmt.Errorf("invalid schema observation state")
		}
	}
	return nil
}
