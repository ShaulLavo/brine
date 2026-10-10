package spec

import (
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type rawRuntime struct {
	UID any `toml:"uid"`
	GID any `toml:"gid"`
}
type rawDatabase struct {
	Name              any `toml:"name"`
	PersistentRoot    any `toml:"persistent_root"`
	MountPath         any `toml:"mount_path"`
	Filename          any `toml:"filename"`
	BackupDestination any `toml:"backup_destination"`
	SyncInterval      any `toml:"sync_interval"`
}
type rawCompatibility struct {
	Database any `toml:"database"`
	Accepts  any `toml:"accepts"`
	Startup  any `toml:"startup"`
}
type rawDefinition struct {
	Database      any `toml:"database"`
	Marker        any `toml:"marker"`
	CatalogSHA256 any `toml:"catalog_sha256"`
}

func validatePersistence(raw rawApp, app *App) error {
	bad := func(field string) error {
		return refusal("spec.invalid_persistence", field, "invalid persistent database declaration")
	}
	if raw.Runtime != nil {
		uid, err := integer(raw.Runtime.UID, "runtime.uid")
		if err != nil {
			return err
		}
		gid, err := integer(raw.Runtime.GID, "runtime.gid")
		if err != nil {
			return err
		}
		if uid < 1 || uid > 65535 || gid < 1 || gid > 65535 {
			return bad("runtime")
		}
		app.Runtime = &RuntimeIdentity{UID: uint32(uid), GID: uint32(gid)}
	}
	if len(raw.Databases) > 16 || (len(raw.Databases) > 0 && app.Runtime == nil) {
		return bad("databases")
	}
	names := map[DatabaseName]bool{}
	for _, r := range raw.Databases {
		if r == nil {
			return bad("databases")
		}
		var d Database
		for _, f := range []struct {
			raw   any
			field string
			set   func(string)
		}{
			{r.Name, "name", func(v string) { d.Name = DatabaseName(v) }},
			{r.PersistentRoot, "persistent_root", func(v string) { d.PersistentRoot = PersistentRoot(v) }},
			{r.MountPath, "mount_path", func(v string) { d.MountPath = ContainerMountPath(v) }},
			{r.Filename, "filename", func(v string) { d.Filename = DatabaseFilename(v) }},
			{r.BackupDestination, "backup_destination", func(v string) { d.BackupDestination = BackupDestinationRef(v) }},
		} {
			v, err := text(f.raw, "databases."+f.field)
			if err != nil {
				return err
			}
			f.set(v)
		}
		d.SyncInterval = data.DefaultSyncInterval
		if r.SyncInterval != nil {
			value, err := text(r.SyncInterval, "databases.sync_interval")
			if err != nil {
				return err
			}
			duration, err := time.ParseDuration(value)
			if err != nil || duration <= 0 {
				return bad("databases.sync_interval")
			}
			d.SyncInterval = duration
		}
		if d.Validate() != nil || names[d.Name] {
			return bad("databases")
		}
		for _, existing := range app.Databases {
			a, b := string(d.MountPath), string(existing.MountPath)
			if a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") {
				return bad("databases.mount_path")
			}
		}
		names[d.Name] = true
		app.Databases = append(app.Databases, d)
	}
	slices.SortFunc(app.Databases, func(a, b Database) int { return strings.Compare(string(a.Name), string(b.Name)) })
	if len(raw.SchemaCompatibility) > 16 {
		return bad("schema_compatibility")
	}
	seen := map[DatabaseName]bool{}
	for _, r := range raw.SchemaCompatibility {
		if r == nil {
			return bad("schema_compatibility")
		}
		name, err := text(r.Database, "schema_compatibility.database")
		if err != nil {
			return err
		}
		startup, err := text(r.Startup, "schema_compatibility.startup")
		if err != nil {
			return err
		}
		reference := DatabaseName(name)
		if !names[reference] || seen[reference] || startup != "preserve" {
			return bad("schema_compatibility")
		}
		markers, ok := r.Accepts.([]any)
		if !ok || len(markers) == 0 || len(markers) > 128 {
			return bad("schema_compatibility.accepts")
		}
		c := SchemaCompatibility{Database: reference, Startup: startup}
		seenMarkers := map[string]bool{}
		for _, rawMarker := range markers {
			marker, err := text(rawMarker, "schema_compatibility.accepts")
			if err != nil {
				return err
			}
			if !data.ValidMarker(marker) || seenMarkers[marker] {
				return bad("schema_compatibility.accepts")
			}
			seenMarkers[marker] = true
			c.Accepts = append(c.Accepts, marker)
		}
		slices.Sort(c.Accepts)
		seen[reference] = true
		app.SchemaCompatibility = append(app.SchemaCompatibility, c)
	}
	slices.SortFunc(app.SchemaCompatibility, func(a, b SchemaCompatibility) int { return strings.Compare(string(a.Database), string(b.Database)) })
	if len(raw.SchemaDefinitions) > 16*128 {
		return bad("schema_definitions")
	}
	definitionSeen := map[[2]string]bool{}
	for _, r := range raw.SchemaDefinitions {
		if r == nil {
			return bad("schema_definitions")
		}
		name, err := text(r.Database, "schema_definitions.database")
		if err != nil {
			return err
		}
		marker, err := text(r.Marker, "schema_definitions.marker")
		if err != nil {
			return err
		}
		hash, err := text(r.CatalogSHA256, "schema_definitions.catalog_sha256")
		if err != nil {
			return err
		}
		key := [2]string{name, marker}
		if !names[DatabaseName(name)] || !data.ValidMarker(marker) || !data.ValidCatalogHash(hash) || definitionSeen[key] || (marker == data.EmptyMarker && hash != data.EmptyCatalogSHA256) {
			return bad("schema_definitions")
		}
		definitionSeen[key] = true
		app.SchemaDefinitions = append(app.SchemaDefinitions, SchemaDefinition{Database: DatabaseName(name), Marker: marker, CatalogSHA256: hash})
	}
	slices.SortFunc(app.SchemaDefinitions, func(a, b SchemaDefinition) int {
		if c := strings.Compare(string(a.Database), string(b.Database)); c != 0 {
			return c
		}
		return strings.Compare(a.Marker, b.Marker)
	})
	return nil
}
