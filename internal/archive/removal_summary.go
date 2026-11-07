package archive

import (
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/target"
	"time"
)

// RemovalSummary exposes only the reviewed retirement effects and point names.
type RemovalSummary struct {
	ArchiveID  data.ArchiveID           `json:"archive_id"`
	ExpiresAt  time.Time                `json:"expires_at"`
	ReleaseID  string                   `json:"release_id"`
	Route      target.CaddyFile         `json:"route"`
	Units      []target.Unit            `json:"units"`
	RetainDays int                      `json:"retain_days"`
	Databases  []RemovalDatabaseSummary `json:"databases"`
}

type RemovalDatabaseSummary struct {
	Name          data.DatabaseName `json:"name"`
	DatabaseID    data.DatabaseID   `json:"database_id"`
	Arm           string            `json:"arm"`
	PreparationID string            `json:"preparation_id,omitempty"`
	SourcePointID string            `json:"source_point_id,omitempty"`
	Cutoff        time.Time         `json:"cutoff,omitempty"`
}

func (s RemovalSummary) Valid() bool {
	if !data.ValidID(string(s.ArchiveID)) || !removalTime(s.ExpiresAt) || s.RetainDays != 30 || len(s.Databases) == 0 || len(s.Databases) > 16 || s.Units == nil {
		return false
	}
	seen := make(map[data.DatabaseID]bool, len(s.Databases))
	for _, d := range s.Databases {
		if !d.valid() || seen[d.DatabaseID] {
			return false
		}
		seen[d.DatabaseID] = true
	}
	return true
}

func (d RemovalDatabaseSummary) valid() bool {
	if !data.ValidID(string(d.DatabaseID)) || !appName.MatchString(string(d.Name)) {
		return false
	}
	if d.Arm == "never_written" {
		return d.PreparationID == "" && d.SourcePointID == "" && d.Cutoff.IsZero()
	}
	return (d.Arm == "prepared_ltx" || d.Arm == "prepared_snapshot") && data.ValidID(d.PreparationID) && data.ValidID(d.SourcePointID) && removalTime(d.Cutoff)
}
