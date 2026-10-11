//go:build linux

package host

import (
	"context"
	"os"
	"reflect"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func (h archiveHost) RecheckArchive(ctx context.Context, p archive.Plan, o archive.Operation, abandon bool) error {
	fresh, err := h.archiveFacts(ctx, p.Candidate.App, o.ID, !abandon)
	if err != nil || !archiveDispositionFactsMatch(p, fresh, abandon) {
		return archive.ErrRefused
	}
	var m archive.Manifest
	if !abandon {
		m, err = h.service.Store.ReadArchiveManifest(ctx, p.ArchiveID)
		if err != nil || !m.Valid() || m.OperationID != o.ID {
			return archive.ErrRefused
		}
	}
	for i, d := range p.Candidate.Databases {
		if !d.Written {
			if _, _, err = data.FreshArchiveAllocation(d.Binding, *d.Allocation); err != nil {
				return archive.ErrRefused
			}
			continue
		}
		observed := data.ObserveSQLiteSchema(ctx, d.Binding, d.Definitions)
		var previous data.SQLiteSchemaObservation
		if !abandon {
			previous = m.Coverage[i].Observation
		}
		if !archiveDispositionSchemaMatches(observed, previous, abandon) {
			return archive.ErrRefused
		}
	}
	if p.Candidate.ReleaseID != "" {
		writer, err := h.service.Store.ReadArchiveWriterSchema(ctx, p.Candidate.Incarnation, o.ID)
		if err != nil || !data.WriterCompatibleWithAllocations(ctx, writer.Bindings, writer.Desired.SchemaCompatibility, writer.Definitions, writer.Allocations) {
			return archive.ErrRefused
		}
	}
	return nil
}

// RetireArchiveRuntime removes only hash-verified managed route/unit artifacts.
// Credentials, configs, journal history and remote epoch objects are retained.
// An uncertain result is never retried by disposition execution.
func (h archiveHost) RetireArchiveRuntime(ctx context.Context, p archive.Plan) error {
	if err := h.VerifyArchiveRuntime(ctx, p); err != nil {
		return err
	}
	if p.Candidate.ReleaseID != "" {
		if h.routes == nil || h.units == nil {
			return archive.ErrRefused
		}
		release, err := h.service.Store.CurrentRelease(ctx, p.Candidate.App)
		if err != nil || release.ID != p.Candidate.ReleaseID || len(release.Units) != 1 {
			return archive.ErrRefused
		}
		unit := release.Units[0]
		if err = h.units.VerifyArchiveRemove(ctx, unit.Name, unit.Hash); err != nil {
			return err
		}
		before, err := h.archiveRouteState(ctx, p)
		if err != nil {
			return err
		}
		outcome, err := h.routes.Withdraw(ctx, before, p.Candidate.App)
		if err != nil || outcome.Outcome != caddy.Applied {
			return archive.ErrRecovery
		}
		stopped, err := h.WritersStopped(ctx, p)
		if err != nil || !stopped {
			return archive.ErrRecovery
		}
		if err = h.units.RemoveArchived(ctx, unit.Name, unit.Hash); err != nil {
			return err
		}
	}
	for _, d := range p.Candidate.Databases {
		for _, b := range d.Epochs {
			if !b.Committed {
				continue
			}
			artifact, hash, err := (replicaRotation{state: h.service.Store, stateRoot: h.stateRoot, home: h.home}).artifact(ctx, b)
			if err != nil || hash != b.UnitSHA256 || data.VerifyRunnerFile(artifact.ServicePath) != nil {
				return archive.ErrRefused
			}
			raw, err := os.ReadFile(artifact.ServicePath)
			if err != nil || string(raw) != string(artifact.Service) {
				return archive.ErrRefused
			}
			name, err := replication.ServiceName(string(b.BindingID))
			if err != nil {
				return err
			}
			stopped, err := h.serviceStopped(ctx, name, false)
			if err != nil || !stopped {
				return archive.ErrRecovery
			}
			if err = os.Remove(artifact.ServicePath); err != nil {
				return err
			}
		}
	}
	result, err := h.session.Execute(ctx, "/usr/bin/systemctl", []string{"--user", "daemon-reload"}, nil, true)
	if err != nil || result.ExitCode != 0 || result.Truncated {
		return archive.ErrRecovery
	}
	// Removed units must now be affirmatively absent, not merely stopped.
	stopped, err := h.serviceStopped(ctx, p.Candidate.App+".service", true)
	if err != nil || !stopped {
		return archive.ErrRecovery
	}
	for _, d := range p.Candidate.Databases {
		for _, b := range d.Epochs {
			name, err := replication.ServiceName(string(b.BindingID))
			if err != nil {
				return err
			}
			stopped, err := h.serviceStopped(ctx, name, true)
			if err != nil || !stopped {
				return archive.ErrRecovery
			}
		}
	}
	return nil
}

func (h archiveHost) ArchiveRuntimeStopped(ctx context.Context, p archive.Plan) (bool, error) {
	// The journaled cleanup phase requires affirmative absence. This observation
	// changes no retained plan identity and grants no stop or startup authority.
	observation := p
	observation.Candidate.ReleaseID = ""
	observation.Trees = append([]data.ArchiveTree(nil), p.Trees...)
	for _, tree := range p.Trees {
		tree.SourceRelative = tree.ArchiveRelative
		observation.Trees = append(observation.Trees, tree)
	}
	policy, err := h.service.Policy.Load(ctx)
	if err != nil || policy.Hash() != p.PolicyHash {
		return false, archive.ErrRefused
	}
	return h.WritersStopped(ctx, observation)
}

func archiveDispositionFactsMatch(planned, observed archive.Plan, abandon bool) bool {
	if observed.TargetHash != planned.TargetHash || len(observed.Candidate.Databases) != len(planned.Candidate.Databases) {
		return false
	}
	if !abandon && (observed.PolicyHash != planned.PolicyHash || observed.Generation != planned.Generation) {
		return false
	}
	candidate := observed.Candidate
	if abandon {
		candidate.Databases = append([]archive.Database(nil), observed.Candidate.Databases...)
		for i := range candidate.Databases {
			candidate.Databases[i].Retention = planned.Candidate.Databases[i].Retention
		}
	}
	return reflect.DeepEqual(candidate, planned.Candidate)
}

// VerifyArchiveRuntime is effect-free and requires every recorded runtime
// artifact, including the route, still present before a safe cleanup exit.
func (h archiveHost) VerifyArchiveRuntime(ctx context.Context, p archive.Plan) error {
	if p.Candidate.ReleaseID != "" {
		if h.routes == nil || h.units == nil {
			return archive.ErrRefused
		}
		release, err := h.service.Store.CurrentRelease(ctx, p.Candidate.App)
		if err != nil || release.ID != p.Candidate.ReleaseID || len(release.Units) != 1 {
			return archive.ErrRefused
		}
		unit := release.Units[0]
		if err = h.units.VerifyArchiveRemove(ctx, unit.Name, unit.Hash); err != nil {
			return err
		}
		if _, err = h.archiveRouteState(ctx, p); err != nil {
			return err
		}
	}
	for _, d := range p.Candidate.Databases {
		for _, binding := range d.Epochs {
			if !binding.Committed {
				continue
			}
			artifact, hash, err := (replicaRotation{state: h.service.Store, stateRoot: h.stateRoot, home: h.home}).artifact(ctx, binding)
			if err != nil || hash != binding.UnitSHA256 || data.VerifyRunnerFile(artifact.ServicePath) != nil {
				return archive.ErrRefused
			}
			raw, err := os.ReadFile(artifact.ServicePath)
			if err != nil || string(raw) != string(artifact.Service) {
				return archive.ErrRefused
			}
		}
	}
	return nil
}
func (h archiveHost) archiveRouteState(ctx context.Context, p archive.Plan) (before caddy.State, err error) {
	release, err := h.service.Store.CurrentRelease(ctx, p.Candidate.App)
	if err != nil || release.ID != p.Candidate.ReleaseID {
		return before, archive.ErrRefused
	}
	snapshot, err := h.service.Inventory.Collect(ctx)
	if err != nil || snapshot.CaddyConfig.Status != target.KnownStatus || snapshot.CaddyConfig.Value == nil {
		return before, archive.ErrRefused
	}
	before = caddy.State{Generation: snapshot.CaddyConfig.Value.Generation, Files: map[string]string{}, Sites: map[string]caddy.Site{}}
	for _, file := range snapshot.CaddyConfig.Value.Files {
		before.Files[file.Name] = file.Hash
	}
	state, err := h.service.Store.LoadBrineState(ctx, snapshot.Identity, p.Generation)
	if err != nil {
		return before, err
	}
	for _, current := range state.Releases {
		if current.HostPort == 0 || current.HostPort > 65535 {
			return before, archive.ErrRefused
		}
		site, err := caddy.CommittedSite(current.Desired, spec.Port(current.HostPort))
		if err != nil {
			return before, err
		}
		before.Sites[current.CaddyFile.Name] = site
	}
	if before.Files[release.CaddyFile.Name] != release.CaddyFile.Hash {
		return before, archive.ErrRefused
	}
	return before, nil
}

func archiveDispositionSchemaMatches(observed, previous data.SQLiteSchemaObservation, abandon bool) bool {
	if observed.UnknownReason != "" || observed.State != data.VerifiedSchema && observed.State != data.VerifiedEmpty {
		return false
	}
	if abandon {
		return true
	}
	return observed.State == previous.State && observed.Marker == previous.Marker && observed.CatalogSHA256 == previous.CatalogSHA256
}
