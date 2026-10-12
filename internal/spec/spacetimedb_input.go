package spec

import (
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/pelletier/go-toml/v2"
)

func ValidateManagedApp(a App) error {
	if a.SpacetimeDB == nil || a.SpacetimeDB.Validate() != nil || len(a.Databases) != 0 || a.Runtime != nil || len(a.SchemaFiles) != 0 || len(a.SchemaArtifacts) != 0 || len(a.Initializers) != 0 || len(a.SchemaDefinitions) != 0 || len(a.RestoreInvariants) != 0 {
		return data.ErrInvalid
	}
	domains := make([]string, len(a.Domains))
	for i, domain := range a.Domains {
		domains[i] = string(domain)
	}
	d := a.SpacetimeDB
	raw := map[string]any{"schema_version": a.SchemaVersion, "name": string(a.Name), "domains": domains,
		"databases": []map[string]any{{"engine": "spacetimedb", "name": string(d.Name), "persistent_root": string(d.PersistentRoot), "backup_destination": string(d.BackupDestination), "recovery_password_ref": string(d.RecoveryPasswordRef), "spacetimedb": d.TOMLValue()}}}
	rows := make([]map[string]any, 0, len(a.SchemaCompatibility))
	for _, c := range a.SchemaCompatibility {
		rows = append(rows, map[string]any{"database": string(c.Database), "accepts": c.Accepts, "startup": c.Startup})
	}
	raw["schema_compatibility"] = rows
	if a.Image != "" || a.ContainerPort != 0 {
		raw["image"], raw["container_port"] = string(a.Image), int(a.ContainerPort)
		raw["environment"], raw["secrets"] = a.Environment, a.Secrets
		raw["health"] = map[string]any{"path": string(a.Health.Path), "expected_status": a.Health.ExpectedStatus, "startup_deadline_seconds": a.Health.StartupDeadlineSeconds, "timeout_seconds": a.Health.TimeoutSeconds}
		if a.Resources != nil {
			raw["resources"] = map[string]int{"memory_mb": a.Resources.MemoryMB, "pids_limit": a.Resources.PIDsLimit}
		}
	} else if len(a.Environment) != 0 || len(a.Secrets) != 0 || a.Resources != nil {
		return data.ErrInvalid
	}
	encoded, err := toml.Marshal(raw)
	if err != nil {
		return data.ErrInvalid
	}
	parsed, err := Parse(encoded)
	if err != nil {
		return err
	}
	if len(a.SchemaCompatibility) != 1 || parsed.SpacetimeDB.SortedAccepts() != d.SortedAccepts() {
		return data.ErrInvalid
	}
	return nil
}

func (d SpacetimeDB) TOMLValue() map[string]any {
	return map[string]any{
		"image": string(d.Image), "version": d.Version, "database": d.Database,
		"module":    map[string]any{"file": d.Module.File, "schema": d.Module.Schema, "marker": d.Module.Marker},
		"runtime":   map[string]any{"uid": d.Runtime.UID, "gid": d.Runtime.GID},
		"resources": d.Resources, "publisher": string(d.Publisher), "signing_private": string(d.SigningPrivate), "signing_public": string(d.SigningPublic), "auth": string(d.Auth),
	}
}
