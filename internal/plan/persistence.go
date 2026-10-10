package plan

import (
	"path/filepath"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/target"
)

const (
	SchemaStateUnknown         ConflictCode = "schema_state_unknown"
	SchemaIncompatible         ConflictCode = "schema_incompatible"
	SchemaRollbackIncompatible ConflictCode = "schema_rollback_incompatible"
	DataFenced                 ConflictCode = "data_fenced"
	DataMappingUnknown         ConflictCode = "data_mapping_unknown"
	BackupRetentionUnknown     ConflictCode = "backup_retention_unknown"
)

func persistentEvidence(in Input, p *Plan, add func(ConflictCode, string)) {
	facts := in.Snapshot.PersistentData
	if facts == nil || facts.Status != target.KnownStatus || facts.Value == nil || len(*facts.Value) != len(in.Desired.Databases) || len(in.Desired.Databases) == 0 {
		add(UnknownFacts, "databases")
		return
	}
	if len(in.Desired.SchemaCompatibility) != len(in.Desired.Databases) {
		add(SchemaCompatibilityRequired, "schema_compatibility")
	}
	seenDeclarations := map[data.DatabaseName]bool{}
	for _, declaration := range in.Desired.Databases {
		if seenDeclarations[declaration.Name] {
			add(SchemaCompatibilityRequired, "databases")
			continue
		}
		seenDeclarations[declaration.Name] = true
		field := "databases." + string(declaration.Name)
		var fact *target.PersistentDatabase
		for i := range *facts.Value {
			candidate := &(*facts.Value)[i]
			if candidate.Database.Name == declaration.Name {
				fact = candidate
			}
		}
		if fact == nil {
			add(UnknownFacts, field)
			continue
		}
		b := fact.Database
		if b.Root != declaration.PersistentRoot || b.MountPath != declaration.MountPath || b.Filename != declaration.Filename || !slices.Contains(in.Desired.PersistentRoots, b.Root) {
			add(PersistentRootDenied, field)
			continue
		}
		if !fact.Root.Admits(b.Root, in.Desired.MinimumFreeDiskBytes) {
			add(InsufficientDisk, field+".root")
		}
		if in.Desired.Runtime == nil || fact.Mapping.Status != target.KnownStatus || fact.Mapping.Value == nil || !fact.Mapping.Value.Admits(*in.Desired.Runtime, fact.Root) {
			add(DataMappingUnknown, field+".mapping")
		}
		var destination *data.Destination
		for i := range in.Desired.BackupDestinations {
			if in.Desired.BackupDestinations[i].Reference == declaration.BackupDestination {
				destination = &in.Desired.BackupDestinations[i]
			}
		}
		protectedRetention := false
		for _, e := range in.Desired.BackupRetention {
			if fact.Retention.Value != nil && e == *fact.Retention.Value {
				protectedRetention = true
			}
		}
		if !protectedRetention || destination == nil || fact.Retention.Status != target.KnownStatus || fact.Retention.Value == nil || !fact.Retention.Value.Admits(*destination, fact.Schema.ObservedAt) {
			add(BackupRetentionUnknown, field+".retention")
		}
		if fact.Fenced {
			add(DataFenced, field)
		}
		if fact.Schema.State == data.Unknown {
			add(SchemaStateUnknown, field+".schema")
		} else {
			compatible := false
			sets := 0
			for _, c := range in.Desired.SchemaCompatibility {
				if c.Database == b.Name {
					sets++
					compatible = completeCompatibility(b.Name, fact.Schema, c, fact.Definitions)
				}
			}
			if !compatible || sets != 1 {
				add(SchemaIncompatible, field+".schema")
			}
			// Automatic compensation may restore only a writer accepting CURRENT data.
			for _, release := range in.State.Releases {
				if release.App != string(in.Desired.Name) {
					continue
				}
				oldCompatible := false
				for _, c := range release.Desired.SchemaCompatibility {
					if c.Database == b.Name {
						oldCompatible = completeCompatibility(b.Name, fact.Schema, c, fact.Definitions)
					}
				}
				if !oldCompatible {
					add(SchemaRollbackIncompatible, field+".schema")
				}
			}
		}
		p.DataMounts = append(p.DataMounts, data.Mount{Database: b, HostPath: filepath.Join(string(b.Root), b.RelativeDirectory), ContainerPath: b.MountPath, BindingID: b.ReplicaBindingID})
	}
}

// Every accepted token must have a retained immutable definition, including
// tokens other than the current schema. Ordering never implies compatibility.
func completeCompatibility(name data.DatabaseName, observation data.SchemaObservation, c data.SchemaCompatibility, definitions []data.SchemaDefinition) bool {
	if !data.CompatibleObservation(name, observation, c) || len(c.Accepts) > 128 {
		return false
	}
	registry := map[string]string{}
	for _, d := range definitions {
		if d.Database != name || !data.ValidMarker(d.Marker) || !data.ValidCatalogHash(d.CatalogSHA256) || registry[d.Marker] != "" {
			return false
		}
		registry[d.Marker] = d.CatalogSHA256
	}
	seen := map[string]bool{}
	for _, marker := range c.Accepts {
		if seen[marker] || registry[marker] == "" {
			return false
		}
		seen[marker] = true
	}
	return registry[observation.Marker] == observation.CatalogSHA256
}
