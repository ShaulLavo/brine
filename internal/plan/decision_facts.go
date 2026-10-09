package plan

import (
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/target"
)

// decisionFacts is an explicit inventory projection. New measurements must be
// classified before they can become freshness preconditions.
type decisionFacts struct {
	SchemaVersion  int                                        `json:"schema_version"`
	Identity       target.Identity                            `json:"identity"`
	OS             target.OS                                  `json:"os"`
	Arch           string                                     `json:"arch"`
	Versions       target.Versions                            `json:"versions"`
	CgroupV2       target.Observation[bool]                   `json:"cgroup_v2"`
	Runner         target.Runner                              `json:"runner"`
	Generation     target.Observation[uint64]                 `json:"generation"`
	CaddyConfig    target.Observation[target.CaddyConfigSet]  `json:"caddy_config"`
	Apps           target.Observation[[]target.App]           `json:"apps"`
	UsedPorts      target.Observation[[]target.Port]          `json:"used_ports"`
	LiveCaddyFiles target.Observation[[]target.LiveCaddyFile] `json:"live_caddy_files"`
	PortOwners     target.Observation[[]target.PortOwner]     `json:"port_owners"`
	SufficientDisk target.Observation[bool]                   `json:"sufficient_disk"`
}

func sufficientDisk(s target.Snapshot, minimum uint64) target.Observation[bool] {
	if s.FreeDiskBytes.Status == target.KnownStatus {
		return target.Known(*s.FreeDiskBytes.Value >= minimum)
	}
	return target.Observation[bool]{Status: s.FreeDiskBytes.Status}
}

// The snapshot has already passed target.Encode's validation and sorting.
func canonicalDecisionFacts(s target.Snapshot, minimum uint64) ([]byte, error) {
	if s.Apps.Value != nil {
		apps := append([]target.App{}, (*s.Apps.Value)...)
		for i := range apps {
			apps[i].UnitActive = nil
		}
		s.Apps = target.Known(apps)
	}
	return json.Marshal(decisionFacts{
		SchemaVersion:  s.SchemaVersion,
		Identity:       s.Identity,
		OS:             s.OS,
		Arch:           s.Arch,
		Versions:       s.Versions,
		CgroupV2:       s.CgroupV2,
		Runner:         s.Runner,
		Generation:     s.Generation,
		CaddyConfig:    s.CaddyConfig,
		Apps:           s.Apps,
		UsedPorts:      s.UsedPorts,
		LiveCaddyFiles: s.LiveCaddyFiles,
		PortOwners:     s.PortOwners,
		SufficientDisk: sufficientDisk(s, minimum),
	})
}
