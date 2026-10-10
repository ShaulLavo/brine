package spec

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
)

type SchemaFile struct {
	Database DatabaseName
	Path     string
	Marker   string
}

func validSchemaPath(p string) bool {
	return p != "" && len(p) <= 4096 && filepath.IsLocal(p) && path.Clean(p) == p && !strings.ContainsAny(p, "\\\x00\r\n")
}

// ParseFile is a client-only boundary. Host dispatch never opens these paths.
func ParseFile(ctx context.Context, filename string, raw []byte) (App, error) {
	app, err := Parse(raw)
	if err != nil {
		return App{}, err
	}
	artifacts := make([]data.SchemaArtifact, 0, len(app.SchemaFiles))
	for _, file := range app.SchemaFiles {
		raw, err := readSchemaFile(filepath.Dir(filename), filepath.FromSlash(file.Path))
		if err != nil {
			return App{}, refusal("spec.schema_file", "databases.schema", "cannot read a bounded regular schema file")
		}
		initializer, err := data.CompileSchemaFile(ctx, file.Database, file.Marker, raw)
		if err != nil {
			return App{}, fmt.Errorf("database %s: %w", file.Database, err)
		}
		artifact, _, err := initializer.Artifact()
		if err != nil {
			return App{}, err
		}
		artifacts = append(artifacts, artifact)
		app.Initializers = append(app.Initializers, initializer)
	}
	if err := app.ResolveSchemas(artifacts); err != nil {
		return App{}, err
	}
	return app, nil
}

func readSchemaFile(directory, filename string) ([]byte, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	// Refuse symlinks in every component, including links within the root.
	// Root.Open additionally enforces confinement during the open itself,
	// even if a component changes after these checks.
	components := strings.Split(filepath.ToSlash(filename), "/")
	for i := range components {
		info, err := root.Lstat(filepath.FromSlash(strings.Join(components[:i+1], "/")))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(components)-1 && !info.IsDir()) {
			return nil, data.ErrInvalid
		}
		if i == len(components)-1 && (!info.Mode().IsRegular() || info.Size() > 1<<20) {
			return nil, data.ErrInvalid
		}
	}
	f, err := root.Open(filename)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, data.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, data.ErrInvalid
	}
	return raw, nil
}

// ResolveSchemas admits only metadata for declared files. Executable initializer
// bytes remain local until the operator installs the reviewed artifact.
func (app *App) ResolveSchemas(artifacts []data.SchemaArtifact) error {
	if len(artifacts) != len(app.SchemaFiles) {
		return data.ErrInvalid
	}
	app.SchemaDefinitions = nil
	app.SchemaArtifacts = nil
	seen := map[DatabaseName]bool{}
	for _, artifact := range artifacts {
		definition := artifact.Definition
		i := slices.IndexFunc(app.SchemaFiles, func(file SchemaFile) bool { return file.Database == definition.Database })
		if i < 0 || seen[definition.Database] || !data.ValidCatalogHash(strings.TrimPrefix(artifact.Digest, "sha256:")) || !strings.HasPrefix(artifact.Digest, "sha256:") {
			return data.ErrInvalid
		}
		file := app.SchemaFiles[i]
		marker := file.Marker
		if marker == "" && data.ValidCatalogHash(definition.CatalogSHA256) {
			marker = "sha256:" + definition.CatalogSHA256[:12]
		}
		if definition.Marker != marker || definition.Marker == data.EmptyMarker || !data.ValidMarker(marker) || !data.ValidCatalogHash(definition.CatalogSHA256) {
			return data.ErrInvalid
		}
		seen[definition.Database] = true
		app.SchemaDefinitions = append(app.SchemaDefinitions, definition)
		app.SchemaArtifacts = append(app.SchemaArtifacts, artifact)
		if !slices.ContainsFunc(app.SchemaCompatibility, func(c SchemaCompatibility) bool { return c.Database == definition.Database }) {
			app.SchemaCompatibility = append(app.SchemaCompatibility, SchemaCompatibility{Database: definition.Database, Accepts: []string{marker}, Startup: "preserve"})
		}
	}
	slices.SortFunc(app.SchemaDefinitions, func(a, b SchemaDefinition) int { return strings.Compare(string(a.Database), string(b.Database)) })
	slices.SortFunc(app.SchemaArtifacts, func(a, b data.SchemaArtifact) int {
		return strings.Compare(string(a.Definition.Database), string(b.Definition.Database))
	})
	slices.SortFunc(app.SchemaCompatibility, func(a, b SchemaCompatibility) int { return strings.Compare(string(a.Database), string(b.Database)) })
	return nil
}

// ValidateDefinitions validates internal retained catalog identities, not TOML.
func ValidateDefinitions(app App) error {
	if len(app.SchemaFiles) > 0 && len(app.SchemaFiles) != len(app.SchemaArtifacts) {
		return data.ErrInvalid
	}
	if len(app.SchemaArtifacts) > 16 {
		return data.ErrInvalid
	}
	if len(app.SchemaDefinitions) > 16*128 {
		return data.ErrInvalid
	}
	seen := map[[2]string]bool{}
	for _, d := range app.SchemaDefinitions {
		key := [2]string{string(d.Database), d.Marker}
		if !slices.ContainsFunc(app.Databases, func(database Database) bool { return database.Name == d.Database }) || !data.ValidMarker(d.Marker) || !data.ValidCatalogHash(d.CatalogSHA256) || seen[key] || (d.Marker == data.EmptyMarker && d.CatalogSHA256 != data.EmptyCatalogSHA256) {
			return data.ErrInvalid
		}
		seen[key] = true
	}
	for _, a := range app.SchemaArtifacts {
		if !slices.Contains(app.SchemaDefinitions, a.Definition) || !strings.HasPrefix(a.Digest, "sha256:") || !data.ValidCatalogHash(strings.TrimPrefix(a.Digest, "sha256:")) {
			return data.ErrInvalid
		}
	}
	return nil
}
