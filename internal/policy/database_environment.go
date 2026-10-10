package policy

import (
	"path"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
)

// DatabaseEnvironmentValues derives public app connection settings from desired input.
// CanonicalBytes regenerates these values so both sources and settings are hash material.
func (d Desired) DatabaseEnvironmentValues() []Environment {
	var environment []Environment
	compatibility := make(map[data.DatabaseName][]string, len(d.SchemaCompatibility))
	for _, c := range d.SchemaCompatibility {
		compatibility[c.Database] = c.Accepts
	}
	for _, database := range d.Databases {
		prefix := "BRINE_DB_" + data.DatabaseEnvironmentName(database.Name)
		accepts := slices.Clone(compatibility[database.Name])
		slices.Sort(accepts)
		environment = append(environment,
			Environment{Name: prefix + "_ENGINE", Value: "sqlite"},
			Environment{Name: prefix + "_PATH", Value: path.Join(string(database.MountPath), string(database.Filename))},
			Environment{Name: prefix + "_ACCEPTS", Value: strings.Join(accepts, ",")},
		)
	}
	slices.SortFunc(environment, func(a, b Environment) int { return strings.Compare(a.Name, b.Name) })
	return environment
}
