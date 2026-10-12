package plan

import "github.com/ShaulLavo/brine/internal/target"

func managedCapabilities(snapshot target.Snapshot, add func(ConflictCode, string)) {
	deploymentCapabilities(snapshot, add)
	caddy := snapshot.Versions.Caddy
	if caddy.Status == target.KnownStatus && caddy.Value != nil && !versionLine(*caddy.Value, "2.10.2") {
		add(RuntimeUnavailable, "managed.caddy_version")
	}
}
