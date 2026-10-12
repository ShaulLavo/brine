package plan

import "github.com/ShaulLavo/brine/internal/target"

func managedCapabilities(snapshot target.Snapshot, add func(ConflictCode, string)) {
	deploymentCapabilitiesWithCaddy(snapshot, add, "2.10.2")
	caddy := snapshot.Versions.Caddy
	if caddy.Status == target.KnownStatus && caddy.Value != nil && !versionLine(*caddy.Value, "2.10.2") {
		add(RuntimeUnavailable, "managed.caddy_version")
	}
}
