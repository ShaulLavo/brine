package plan

import (
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/target"
)

// decisionFacts is an explicit inventory projection. New measurements must be
// classified before they can become freshness preconditions.
type decisionFacts struct {
	PersistentData *target.Observation[[]target.PersistentDatabase] `json:"persistent_data,omitempty"`
	SchemaVersion  int                                              `json:"schema_version"`
	Identity       target.Identity                                  `json:"identity"`
	OS             target.OS                                        `json:"os"`
	Arch           string                                           `json:"arch"`
	Versions       target.Versions                                  `json:"versions"`
	CgroupV2       target.Observation[bool]                         `json:"cgroup_v2"`
	Runner         target.Runner                                    `json:"runner"`
	Generation     target.Observation[uint64]                       `json:"generation"`
	CaddyConfig    target.Observation[target.CaddyConfigSet]        `json:"caddy_config"`
	Apps           target.Observation[[]decisionApp]                `json:"apps"`
	UsedPorts      target.Observation[[]target.Port]                `json:"used_ports"`
	LiveCaddyFiles target.Observation[[]target.LiveCaddyFile]       `json:"live_caddy_files"`
	PortOwners     target.Observation[[]target.PortOwner]           `json:"port_owners"`
	SufficientDisk target.Observation[bool]                         `json:"sufficient_disk"`
}

func sufficientDisk(s target.Snapshot, minimum uint64) target.Observation[bool] {
	if s.FreeDiskBytes.Status == target.KnownStatus {
		return target.Known(*s.FreeDiskBytes.Value >= minimum)
	}
	return target.Observation[bool]{Status: s.FreeDiskBytes.Status}
}

// Other apps only contribute their observed port to admission. The planned app
// retains all ownership observations, including unknown image evidence.
type decisionApp struct {
	Name              string                               `json:"name"`
	Image             *target.Observation[target.Image]    `json:"image,omitempty"`
	AllocatedHostPort target.Observation[target.Port]      `json:"allocated_host_port"`
	QuadletUnits      *target.Observation[[]target.Unit]   `json:"quadlet_units,omitempty"`
	Secrets           *target.Observation[[]target.Secret] `json:"secrets,omitempty"`
}

// The snapshot has already passed target.Encode's validation and sorting.
func canonicalDecisionFacts(in Input) ([]byte, error) {
	s := portAdmissionSnapshot(in)
	minimum := in.Desired.MinimumFreeDiskBytes
	apps := target.Observation[[]decisionApp]{Status: s.Apps.Status}
	if s.Apps.Value != nil {
		projected := make([]decisionApp, 0, len(*s.Apps.Value))
		for _, app := range *s.Apps.Value {
			a := decisionApp{Name: app.Name, AllocatedHostPort: app.AllocatedHostPort}
			if app.Name == string(in.Desired.Name) {
				a.Image = &app.Image
				a.QuadletUnits = &app.QuadletUnits
				a.Secrets = &app.Secrets
			}
			projected = append(projected, a)
		}
		apps.Value = &projected
	}
	if s.PersistentData != nil && s.PersistentData.Value != nil {
		raw, err := json.Marshal(s.PersistentData)
		if err != nil {
			return nil, err
		}
		var copy target.Observation[[]target.PersistentDatabase]
		if err = json.Unmarshal(raw, &copy); err != nil {
			return nil, err
		}
		for i := range *copy.Value {
			f := &(*copy.Value)[i]
			f.Root.OperatorQuota = nil                                              // Reporting only; no quota verdict is inferred.
			f.Usage = target.Observation[data.StorageUsage]{Status: target.Unknown} // Reporting-only; never a quota admission verdict.
			if f.Root.FreeBytes >= minimum {
				f.Root.FreeBytes = minimum
			}
			if f.Root.FreeInodes > 0 {
				f.Root.FreeInodes = 1
			}
			f.Root.ObservedAt = time.Time{}
			if f.Mapping.Value != nil {
				f.Mapping.Value.ObservedAt = time.Time{}
			}
			if f.Credentials.Value != nil && !f.Credentials.Value.Admits(f.Database.ReplicaBindingID, f.Credentials.Value.PolicyHash, f.Schema.ObservedAt, time.Minute) {
				f.Credentials = target.Observation[data.CredentialEvidence]{Status: target.Unknown}
			}
			// The retention decision is time-sensitive, its observation timestamp is not.
			if f.Retention.Value != nil {
				e := f.Retention.Value
				at, _ := time.Parse(time.RFC3339Nano, e.VerifiedAt)
				if !e.Valid() || f.Schema.ObservedAt.Before(at) || f.Schema.ObservedAt.Sub(at) > time.Duration(e.FreshnessSeconds)*time.Second { //nolint:gosec // Valid bounds freshness to at most 30 days.
					copy.Status = target.Unknown
					copy.Value = nil
					break
				}
			}
			f.Schema.ObservedAt = time.Time{}
		}
		s.PersistentData = &copy
	}
	return json.Marshal(decisionFacts{
		PersistentData: s.PersistentData,
		SchemaVersion:  s.SchemaVersion,
		Identity:       s.Identity,
		OS:             s.OS,
		Arch:           s.Arch,
		Versions:       s.Versions,
		CgroupV2:       s.CgroupV2,
		Runner:         s.Runner,
		Generation:     s.Generation,
		CaddyConfig:    s.CaddyConfig,
		Apps:           apps,
		UsedPorts:      s.UsedPorts,
		LiveCaddyFiles: s.LiveCaddyFiles,
		PortOwners:     s.PortOwners,
		SufficientDisk: sufficientDisk(s, minimum),
	})
}

// Inventory remains policy-independent. Admission and freshness share this
// projection, including a retained app port outside a revised allocation range.
func portAdmissionSnapshot(in Input) target.Snapshot {
	s := in.Snapshot
	relevant := func(port target.Port) bool {
		if port == 80 || port == 443 || port == 2019 || (port >= target.Port(in.Desired.AppPorts.Min) && port <= target.Port(in.Desired.AppPorts.Max)) {
			return true
		}
		if s.Apps.Value != nil {
			for _, app := range *s.Apps.Value {
				if app.Name == string(in.Desired.Name) && app.AllocatedHostPort.Value != nil && *app.AllocatedHostPort.Value == port {
					return true
				}
			}
		}
		for _, release := range in.State.Releases {
			if release.App == string(in.Desired.Name) && release.HostPort == port {
				return true
			}
		}
		return false
	}
	if s.UsedPorts.Value != nil {
		ports := []target.Port{}
		for _, port := range *s.UsedPorts.Value {
			if relevant(port) {
				ports = append(ports, port)
			}
		}
		s.UsedPorts.Value = &ports
	}
	if s.PortOwners.Value != nil {
		owners := []target.PortOwner{}
		for _, owner := range *s.PortOwners.Value {
			if relevant(owner.Port) {
				owners = append(owners, owner)
			}
		}
		s.PortOwners.Value = &owners
	}
	return s
}
