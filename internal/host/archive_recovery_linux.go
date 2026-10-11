//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

type archiveRecoveryUnits interface {
	InspectArchiveRemove(context.Context, string, string) (bool, error)
}
type recoveryReplica struct {
	path    string
	present bool
}
type recoveryRuntime struct {
	writerName, writerHash      string
	writerPresent, routePresent bool
	replicas                    []recoveryReplica
	before, observed            caddy.State
}

func (h archiveHost) recoveryRuntime(ctx context.Context, p archive.Plan) (out recoveryRuntime, err error) {
	if p.Candidate.ReleaseID != "" {
		units, ok := h.units.(archiveRecoveryUnits)
		if !ok || h.routes == nil {
			return out, archive.ErrRefused
		}
		release, err := h.service.Store.CurrentRelease(ctx, p.Candidate.App)
		if err != nil || release.ID != p.Candidate.ReleaseID || len(release.Units) != 1 {
			return out, archive.ErrRefused
		}
		out.writerName, out.writerHash = release.Units[0].Name, release.Units[0].Hash
		out.writerPresent, err = units.InspectArchiveRemove(ctx, out.writerName, out.writerHash)
		if err != nil {
			return out, err
		}
		snapshot, err := h.service.Inventory.Collect(ctx)
		if err != nil || snapshot.CaddyConfig.Status != target.KnownStatus || snapshot.CaddyConfig.Value == nil {
			return out, archive.ErrRefused
		}
		cfg := snapshot.CaddyConfig.Value
		out.observed = caddy.State{Generation: cfg.Generation, Files: map[string]string{}, Sites: map[string]caddy.Site{}}
		for _, file := range cfg.Files {
			if _, exists := out.observed.Files[file.Name]; exists {
				return out, archive.ErrRefused
			}
			out.observed.Files[file.Name] = file.Hash
		}
		state, err := h.service.Store.LoadBrineState(ctx, snapshot.Identity, p.Generation)
		if err != nil {
			return out, err
		}
		wanted := map[string]string{}
		sites := map[string]caddy.Site{}
		for _, current := range state.Releases {
			if current.HostPort == 0 || current.HostPort > 65535 {
				return out, archive.ErrRefused
			}
			site, err := caddy.CommittedSite(current.Desired, spec.Port(current.HostPort))
			if err != nil {
				return out, err
			}
			wanted[current.CaddyFile.Name] = current.CaddyFile.Hash
			sites[current.CaddyFile.Name] = site
		}
		actual, found := out.observed.Files[release.CaddyFile.Name]
		out.routePresent = found
		if found && actual != release.CaddyFile.Hash {
			return out, archive.ErrRefused
		}
		if !found {
			delete(wanted, release.CaddyFile.Name)
		}
		if !maps.Equal(wanted, out.observed.Files) {
			return out, archive.ErrRefused
		}
		out.before = caddy.State{Generation: cfg.Generation, Files: maps.Clone(out.observed.Files), Sites: sites}
		if !found {
			if cfg.Generation == 0 {
				return out, archive.ErrRefused
			}
			out.before.Generation--
			out.before.Files[release.CaddyFile.Name] = release.CaddyFile.Hash
		}
	}
	for _, d := range p.Candidate.Databases {
		for _, b := range d.Epochs {
			if !b.Committed {
				continue
			}
			artifact, hash, err := (replicaRotation{state: h.service.Store, stateRoot: h.stateRoot, home: h.home}).artifact(ctx, b)
			if err != nil || hash != b.UnitSHA256 {
				return out, archive.ErrRefused
			}
			for _, relative := range []string{".config", ".config/systemd", ".config/systemd/user"} {
				if _, err = data.InspectRoot(filepath.Join(h.home, relative)); err != nil {
					return out, archive.ErrRefused
				}
			}
			_, err = os.Lstat(artifact.ServicePath)
			present := err == nil
			if err != nil && !os.IsNotExist(err) {
				return out, archive.ErrRefused
			}
			if present {
				if data.VerifyRunnerFile(artifact.ServicePath) != nil {
					return out, archive.ErrRefused
				}
				// #nosec G304 -- ID-derived rendered service path, verified private ancestry and runner file above.
				raw, err := os.ReadFile(artifact.ServicePath)
				if err != nil || string(raw) != string(artifact.Service) {
					return out, archive.ErrRefused
				}
			}
			out.replicas = append(out.replicas, recoveryReplica{path: artifact.ServicePath, present: present})
		}
	}
	return out, nil
}
func (h archiveHost) InspectArchiveRuntime(ctx context.Context, p archive.Plan) (archive.RuntimeInspection, error) {
	r, err := h.recoveryRuntime(ctx, p)
	if err != nil {
		return archive.RuntimeInspection{}, err
	}
	complete := !r.writerPresent && !r.routePresent
	for _, replica := range r.replicas {
		complete = complete && !replica.present
	}
	return archive.RuntimeInspection{Complete: complete}, nil
}
func (h archiveHost) ReconcileArchiveRuntime(ctx context.Context, p archive.Plan) (err error) {
	// Inspect every artifact before any cleanup effect, including partial cleanup.
	r, err := h.recoveryRuntime(ctx, p)
	if err != nil {
		return err
	}
	if p.Candidate.ReleaseID != "" {
		if r.routePresent {
			outcome, err := h.routes.Withdraw(ctx, r.before, p.Candidate.App)
			if err != nil || outcome.Outcome != caddy.Applied {
				return archive.ErrRecovery
			}
		} else if h.routes.SettleWithdrawal(ctx, r.before, r.observed, p.Candidate.App) != nil {
			return archive.ErrRecovery
		}
		if r.writerPresent && h.units.RemoveArchived(ctx, r.writerName, r.writerHash) != nil {
			return archive.ErrRecovery
		}
	}
	root, err := os.OpenRoot(h.home)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	var replicaParent *os.Root
	for _, replica := range r.replicas {
		if replica.present {
			replicaParent, err = root.OpenRoot(".config/systemd/user")
			if err != nil {
				return archive.ErrRecovery
			}
			break
		}
	}
	if replicaParent != nil {
		defer func() { err = errors.Join(err, replicaParent.Close()) }()
	}
	for _, replica := range r.replicas {
		if !replica.present {
			continue
		}
		relative, err := filepath.Rel(h.home, replica.path)
		if err != nil || filepath.Dir(relative) != ".config/systemd/user" {
			return archive.ErrRefused
		}
		if err = replicaParent.Remove(filepath.Base(relative)); err != nil {
			return archive.ErrRecovery
		}
	}
	if replicaParent != nil {
		directory, err := replicaParent.Open(".")
		if err != nil {
			return archive.ErrRecovery
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil || closeErr != nil {
			return archive.ErrRecovery
		}
	}
	result, err := h.session.Execute(ctx, "/usr/bin/systemctl", []string{"--user", "daemon-reload"}, nil, true)
	if err != nil || result.ExitCode != 0 || result.Truncated {
		return archive.ErrRecovery
	}
	stopped, err := h.ArchiveRuntimeStopped(ctx, p)
	if err != nil || !stopped {
		return archive.ErrRecovery
	}
	for _, d := range p.Candidate.Databases {
		for _, b := range d.Epochs {
			name, err := replication.ServiceName(string(b.BindingID))
			if err != nil {
				return err
			}
			stopped, err = h.serviceStopped(ctx, name, true)
			if err != nil || !stopped {
				return archive.ErrRecovery
			}
		}
	}
	inspected, err := h.InspectArchiveRuntime(ctx, p)
	if err != nil || !inspected.Complete {
		return archive.ErrRecovery
	}
	return nil
}
func (h archiveHost) recoveryQuiescent(ctx context.Context, p archive.Plan) error {
	names := []string{p.Candidate.App + ".service"}
	for _, d := range p.Candidate.Databases {
		for _, b := range d.Epochs {
			name, err := replication.ServiceName(string(b.BindingID))
			if err != nil {
				return err
			}
			names = append(names, name)
		}
	}
	for _, name := range names {
		stopped, err := h.serviceStopped(ctx, name, false)
		if err != nil {
			return archive.ErrRefused
		}
		if !stopped {
			stopped, err = h.serviceStopped(ctx, name, true)
		}
		if err != nil || !stopped {
			return archive.ErrRefused
		}
	}
	both := p
	both.Trees = append([]data.ArchiveTree(nil), p.Trees...)
	for _, tree := range p.Trees {
		tree.SourceRelative = tree.ArchiveRelative
		both.Trees = append(both.Trees, tree)
	}
	stopped, err := h.archiveContainersStopped(ctx, both)
	if err != nil || !stopped {
		return archive.ErrRefused
	}
	return nil
}
func (h archiveHost) CheckArchiveRecovery(ctx context.Context, p archive.Plan, o archive.Operation, m archive.Manifest) error {
	pol, err := h.service.Policy.Load(ctx)
	if err != nil {
		return archive.ErrRefused
	}
	current, exists, err := h.service.Store.ReadArchiveOperation(ctx, p.ID)
	if err != nil || !exists || !reflect.DeepEqual(current, o) {
		return archive.ErrRefused
	}
	state, exists, err := h.service.Store.ReadArchiveDisposition(ctx, p.ArchiveID)
	if err != nil || !exists {
		return archive.ErrRefused
	}
	inspected, err := h.InspectArchiveRuntime(ctx, p)
	if err != nil || state.Stage != archive.CleanupIntent && !inspected.Complete {
		return archive.ErrRefused
	}
	for _, f := range current.Fences {
		if f.State != data.FenceHeld {
			return archive.ErrRefused
		}
	}
	candidate, err := h.service.Store.ArchiveOwnedCandidate(ctx, p.Candidate.App, o.ID)
	if err != nil || len(candidate.Databases) != len(p.Candidate.Databases) {
		return archive.ErrRefused
	}
	for i := range candidate.Databases {
		candidate.Databases[i].Retention = p.Candidate.Databases[i].Retention
	}
	if !reflect.DeepEqual(candidate, p.Candidate) {
		return archive.ErrRefused
	}
	snapshot, err := h.service.Inventory.Collect(ctx)
	if err != nil {
		return archive.ErrRefused
	}
	raw, err := json.Marshal(snapshot.Identity)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if p.TargetHash != "sha256:"+hex.EncodeToString(sum[:]) || h.recoveryQuiescent(ctx, p) != nil {
		return archive.ErrRefused
	}
	for i, d := range p.Candidate.Databases {
		if !slices.Contains(pol.PersistentRoots(), string(d.Binding.Root)) || d.Allocation == nil {
			return archive.ErrRefused
		}
		for _, b := range d.Epochs {
			destination, ok := pol.BackupDestination(b.Destination.Reference)
			if !ok || destination != b.Destination {
				return archive.ErrRefused
			}
			retention, ok := pol.BackupRetention(b.Destination.Reference)
			if !ok || !retention.Admits(destination, time.Now().UTC()) {
				return archive.ErrRefused
			}
		}
		archived := data.VerifyArchivedAllocation(d.Binding, *d.Allocation, p.ArchiveID) == nil
		if !archived && data.VerifyArchiveAllocation(d.Binding, *d.Allocation) != nil {
			return archive.ErrRefused
		}
		if !d.Written {
			if archived {
				err = data.VerifyArchivedEmpty(d.Binding, *d.Allocation, p.ArchiveID)
			} else {
				_, _, err = data.FreshArchiveAllocation(d.Binding, *d.Allocation)
			}
			if err != nil {
				return archive.ErrRefused
			}
			continue
		}
		observed := data.ObserveSQLiteSchema(ctx, d.Binding, d.Definitions)
		if archived {
			observed = data.ObserveArchivedSchema(ctx, d.Binding, *d.Allocation, p.ArchiveID, d.Definitions)
		}
		if !archiveDispositionSchemaMatches(observed, m.Coverage[i].Observation, false) {
			return archive.ErrRefused
		}
		if err = h.recheckRecoverySource(ctx, p, d, o.ID, m.Coverage[i]); err != nil {
			return err
		}
	}
	return h.recoveryQuiescent(ctx, p)
}
func (h archiveHost) recheckRecoverySource(ctx context.Context, p archive.Plan, d archive.Database, operation string, coverage archive.Coverage) error {
	if at, ok := h.recoveryVerified[string(p.ArchiveID)+":"+string(d.Binding.DatabaseID)]; ok && !time.Now().Before(at) && time.Since(at) < 2*time.Minute {
		return nil
	}
	if len(d.Epochs) != 1 || coverage.Point == nil || coverage.Receipt == nil {
		return archive.ErrRefused
	}
	b := d.Epochs[0]
	credentials, err := h.archiveCredentials(ctx, b)
	if err != nil {
		return err
	}
	defer func() { credentials = restore.Credentials{} }()
	binding := restore.Binding{ID: string(b.BindingID), Epoch: string(b.EpochID), CredentialRef: b.Destination.CredentialRef, Destination: restore.Destination{Endpoint: b.Destination.Endpoint, Region: b.Destination.Region, Bucket: b.Destination.Bucket, Prefix: strings.TrimSuffix(b.RemotePrefix, "/"), PathStyle: b.Destination.PathStyle}}
	request := requestAtPoint(restore.SQLiteRequest{OperationID: fmt.Sprintf("%s-recovery-%s-%d", operation, d.Binding.DatabaseID, time.Now().UnixNano()), Budget: 3 * time.Minute}, *coverage.Point)
	request.Coverage = &restore.CoverageEvidence{Source: request.Source, CoveredThrough: coverage.WritersQuiescedAt}
	for _, invariant := range d.Invariants {
		request.Invariants = append(request.Invariants, restore.Invariant{Kind: restore.InvariantKind(invariant.Kind), Table: invariant.Table, Column: invariant.Column, Count: invariant.Count, Minimum: invariant.Minimum, Maximum: invariant.Maximum})
	}
	if err = ensurePrivateChild(h.stateRoot, "archive-restores"); err != nil {
		return err
	}
	engine := h.archiveRestoreEngine(d, binding, credentials)
	receipt, err := restore.VerifySQLite(ctx, &engine, request)
	if err != nil || receipt.OperationID != request.OperationID || !archiveRecoveryReceiptMatches(coverage, receipt, time.Now().UTC()) || len(d.Invariants) > 0 && receipt.InvariantCheck != "passed" {
		return archive.ErrRefused
	}
	if h.recoveryVerified != nil {
		h.recoveryVerified[string(p.ArchiveID)+":"+string(d.Binding.DatabaseID)] = receipt.ObservedAt
	}
	return nil
}

func archiveRecoveryReceiptMatches(coverage archive.Coverage, receipt restore.SQLiteReceipt, now time.Time) bool {
	return coverage.Receipt != nil && receipt.Valid() && !now.Before(receipt.ObservedAt) && now.Sub(receipt.ObservedAt) <= archive.MaxEvidenceAge && reflect.DeepEqual(receipt.Source, coverage.Receipt.Source) && receipt.Schema.State == restore.SchemaState(coverage.Observation.State) && receipt.Schema.Marker == coverage.Observation.Marker && receipt.Schema.CatalogSHA256 == coverage.Observation.CatalogSHA256 && receipt.Coverage != nil && receipt.Coverage.CoveredThrough.Equal(coverage.WritersQuiescedAt) && receipt.LossWindow.State == restore.LossBounded
}
