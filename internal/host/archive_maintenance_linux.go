//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/restore"
)

// Retired maintenance checks original binding identities and both original and
// archived mount paths, never the app-name service a fresh incarnation may own.
func (h archiveHost) maintenanceQuiescent(ctx context.Context, p archive.Plan) error {
	for _, variant := range p.Candidate.Databases {
		if native, ok := variant.Native(); ok {
			if err := h.nativeArchiveMaintenanceQuiescent(ctx, p, native); err != nil {
				return err
			}
			continue
		}
		database, sqlite := variant.SQLite()
		if !sqlite {
			return archive.ErrRefused
		}
		for _, binding := range database.Epochs {
			name, err := replication.ServiceName(string(binding.BindingID))
			if err != nil {
				return archive.ErrRefused
			}
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
func (h archiveHost) ArchiveMaintenanceAdmission(ctx context.Context, p archive.Plan, d archive.Disposition) (out archive.MaintenanceAdmission, err error) {
	if !p.Valid() || d.Stage != archive.Retired || d.ArchiveID != p.ArchiveID || !d.RetainUntil.Equal(d.CommittedAt.Add(30*24*time.Hour)) {
		return out, archive.ErrRefused
	}
	original, err := h.service.Store.LoadArchivePlanByArchive(ctx, p.ArchiveID)
	if err != nil || !reflect.DeepEqual(original, p) {
		return out, archive.ErrRefused
	}
	retired, exists, err := h.service.Store.ReadArchiveDisposition(ctx, p.ArchiveID)
	if err != nil || !exists || !reflect.DeepEqual(retired, d) {
		return out, archive.ErrRefused
	}
	manifest, err := h.service.Store.ReadArchiveManifest(ctx, p.ArchiveID)
	if err != nil || !manifest.Valid() || manifest.Plan.ID != p.ID {
		return out, archive.ErrRefused
	}
	pol, err := h.service.Policy.Load(ctx)
	if err != nil {
		return out, archive.ErrRefused
	}
	snapshot, err := h.service.Inventory.Collect(ctx)
	if err != nil {
		return out, archive.ErrRefused
	}
	raw, err := json.Marshal(snapshot.Identity)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(raw)
	out.TargetHash = "sha256:" + hex.EncodeToString(sum[:])
	out.PolicyHash = pol.Hash()
	out.PurgeAllowed = pol.AllowsArchivePurge()
	out.RetentionAdmitted = true
	if out.TargetHash != p.TargetHash {
		return out, archive.ErrRefused
	}
	for _, variant := range p.Candidate.Databases {
		if native, ok := variant.Native(); ok {
			admitted, err := h.nativeArchiveMaintenanceAdmission(ctx, p, manifest, native, pol)
			if err != nil {
				return out, err
			}
			out.RetentionAdmitted = out.RetentionAdmitted && admitted
			continue
		}
		database, sqlite := variant.SQLite()
		if !sqlite {
			return out, archive.ErrRefused
		}
		if database.Allocation == nil || !slices.Contains(pol.PersistentRoots(), string(database.Binding.Root)) {
			return out, archive.ErrRefused
		}
		root, err := data.InspectRoot(string(database.Binding.Root))
		if err == nil {
			root, err = root.WithPreparationProof(database.Allocation.RootProof)
		}
		if err != nil || !root.Admits(database.Binding.Root, 0) {
			return out, archive.ErrRefused
		}
		permit, err := h.service.Store.ReadReplicaPermit(ctx, database.Binding.DatabaseID)
		if err != nil || permit.FenceState != "held" {
			return out, archive.ErrRefused
		}
		held := 0
		for _, fence := range permit.Fences {
			if fence.State != data.FenceHeld {
				continue
			}
			if fence.OperationID != manifest.OperationID || fence.DatabaseID != database.Binding.DatabaseID || fence.IncarnationID != p.Candidate.Incarnation {
				return out, archive.ErrRefused
			}
			held++
		}
		if held != 1 {
			return out, archive.ErrRefused
		}
		for _, binding := range database.Epochs {
			destination, ok := pol.BackupDestination(binding.Destination.Reference)
			if !ok || destination != binding.Destination {
				return out, archive.ErrRefused
			}
			if database.Written {
				retention, known := pol.BackupRetention(binding.Destination.Reference)
				if !known || !retention.Admits(binding.Destination, time.Now().UTC()) {
					out.RetentionAdmitted = false
				}
			}
		}
	}
	if h.maintenanceQuiescent(ctx, p) != nil {
		return out, archive.ErrRefused
	}
	return out, nil
}
func (h archiveHost) DeleteArchivedTree(ctx context.Context, p archive.Plan, reviewed archive.MaintenancePlan, index int, adopt bool) error {
	if index < 0 || index >= len(p.Trees) || len(reviewed.Parents) != len(p.Trees) || reviewed.ArchiveID != p.ArchiveID || reviewed.ArchivePlanID != p.ID {
		return archive.ErrRefused
	}
	return data.DeleteArchiveTree(ctx, p.Trees[index], p.Candidate.Incarnation, p.ArchiveID, reviewed.Parents[index], adopt)
}

func (h archiveHost) archiveCredentialScope(ctx context.Context, id, binding, epoch string) (out backupcredentials.ArchiveScope, err error) {
	if !data.ValidID(id) || !data.ValidID(binding) || !data.ValidID(epoch) {
		return out, archive.ErrRefused
	}
	p, err := h.service.Store.LoadArchivePlanByArchive(ctx, data.ArchiveID(id))
	if err != nil {
		return out, archive.ErrRefused
	}
	d, exists, err := h.service.Store.ReadArchiveDisposition(ctx, p.ArchiveID)
	if err != nil || !exists || !time.Now().Before(d.RetainUntil) {
		return out, archive.ErrRefused
	}
	admission, err := h.ArchiveMaintenanceAdmission(ctx, p, d)
	if err != nil {
		return out, err
	}
	cursor, err := h.service.Store.ReadArchiveDeletion(ctx, p.ArchiveID)
	if err != nil || cursor.Sequence != 0 {
		return out, archive.ErrRefused
	}
	for _, tree := range p.Trees {
		if data.VerifyArchivedTree(tree, p.Candidate.Incarnation, p.ArchiveID) != nil {
			return out, archive.ErrRefused
		}
	}
	for _, variant := range p.Candidate.Databases {
		for _, recorded := range variant.ArchiveBindings() {
			if string(recorded.BindingID) == binding && string(recorded.EpochID) == epoch {
				return backupcredentials.ArchiveScope{ArchiveID: id, Binding: binding, Epoch: epoch, CredentialRef: recorded.Destination.CredentialRef, Destination: string(recorded.Destination.Reference), TargetHash: admission.TargetHash, PolicyHash: admission.PolicyHash, RetainUntil: d.RetainUntil}, nil
			}
		}
	}
	return out, archive.ErrRefused
}

func (h archiveHost) retainedCredentials(ctx context.Context, id data.ArchiveID, binding data.ReplicaBinding, deadline time.Time) (out restore.Credentials, validUntil time.Time, err error) {
	r, err := h.service.Store.ReadArchiveCredentialReceipt(ctx, id, binding.BindingID, binding.EpochID)
	if errors.Is(err, sql.ErrNoRows) {
		// The exact original credential version remains authorized until renewed;
		// this is not an app-name/latest-version fallback or version-skew path.
		out, err = h.archiveCredentials(ctx, binding)
		validUntil = deadline
		if !out.ExpiresAt.IsZero() && out.ExpiresAt.Before(validUntil) {
			validUntil = out.ExpiresAt
		}
		out.ExpiresAt = validUntil
		return out, validUntil, err
	}
	if err != nil || r.Plan.Scope.CredentialRef != binding.Destination.CredentialRef || r.Plan.Scope.Destination != string(binding.Destination.Reference) || !r.Plan.Scope.RetainUntil.Equal(deadline) || r.Admit(time.Now().UTC(), 3*time.Minute) != nil {
		return out, validUntil, archive.ErrRefused
	}
	files := backupcredentials.Files{Root: filepath.Join(h.stateRoot, "credentials")}
	sealed, err := files.ArchiveReceipt(r.Plan.Scope.FileReference(), r.Plan.Version)
	if err != nil || !reflect.DeepEqual(sealed, r) {
		return out, validUntil, archive.ErrRefused
	}
	packet, err := files.Read(r.Plan.Scope.FileReference(), r.Plan.Version)
	if err != nil {
		return out, validUntil, archive.ErrRefused
	}
	defer packet.Clear()
	out.ReceivedAt = r.ReceivedAt
	validUntil = deadline
	if r.Plan.ExpiresAt != nil {
		out.ExpiresAt = *r.Plan.ExpiresAt
		if out.ExpiresAt.Before(validUntil) {
			validUntil = out.ExpiresAt
		}
	}
	environment := packet.Environment()
	defer clear(environment)
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "AWS_ACCESS_KEY_ID":
			out.AccessKey = value
		case "AWS_SECRET_ACCESS_KEY":
			out.SecretKey = value
		case "AWS_SESSION_TOKEN":
			out.SessionToken = value
		}
	}
	// The restore budget must fit both issuer expiry and the immutable archive deadline.
	out.ExpiresAt = validUntil
	return out, validUntil, nil
}
func (h archiveHost) VerifyArchiveRetention(ctx context.Context, p archive.Plan, m archive.Manifest, d archive.Disposition) (out archive.RetentionProof, err error) {
	if !m.Valid() || m.Plan.ID != p.ID {
		return out, archive.ErrRefused
	}
	if _, err = h.ArchiveMaintenanceAdmission(ctx, p, d); err != nil {
		return out, err
	}
	pol, err := h.service.Policy.Load(ctx)
	if err != nil {
		return out, archive.ErrRefused
	}
	validUntil := d.RetainUntil
	for i, variant := range p.Candidate.Databases {
		database, sqlite := variant.SQLite()
		if !sqlite {
			return out, archive.ErrRefused
		}
		coverage, sqliteCoverage := m.Coverage[i].SQLite()
		if !sqliteCoverage {
			return out, archive.ErrRefused
		}
		if database.Allocation == nil || data.VerifyArchivedAllocation(database.Binding, *database.Allocation, p.ArchiveID) != nil {
			return out, archive.ErrRefused
		}
		if !database.Written {
			if data.VerifyArchivedEmpty(database.Binding, *database.Allocation, p.ArchiveID) != nil {
				return out, archive.ErrRefused
			}
			continue
		}
		if len(database.Epochs) != 1 || coverage.Point == nil || coverage.Receipt == nil {
			return out, archive.ErrRefused
		}
		observed := data.ObserveArchivedSchema(ctx, database.Binding, *database.Allocation, p.ArchiveID, database.Definitions)
		if !archiveDispositionSchemaMatches(observed, coverage.Observation, false) {
			return out, archive.ErrRefused
		}
		binding := database.Epochs[0]
		retention, ok := pol.BackupRetention(binding.Destination.Reference)
		if !ok || !retention.Admits(binding.Destination, time.Now().UTC()) {
			return out, archive.ErrRefused
		}
		verifiedAt, err := time.Parse(time.RFC3339Nano, retention.VerifiedAt)
		if err != nil {
			return out, archive.ErrRefused
		}
		// RetentionEvidence.Valid bounds this conversion to thirty days.
		retentionUntil := verifiedAt.Add(time.Duration(retention.FreshnessSeconds) * time.Second) //nolint:gosec // Admits checked the bounded freshness value.
		if retentionUntil.Before(validUntil) {
			validUntil = retentionUntil
		}
		credentials, credentialsUntil, err := h.retainedCredentials(ctx, p.ArchiveID, binding, d.RetainUntil)
		if err != nil {
			return out, err
		}
		if credentialsUntil.Before(validUntil) {
			validUntil = credentialsUntil
		}
		remote := restore.Binding{ID: string(binding.BindingID), Epoch: string(binding.EpochID), CredentialRef: binding.Destination.CredentialRef, Destination: restore.Destination{Endpoint: binding.Destination.Endpoint, Region: binding.Destination.Region, Bucket: binding.Destination.Bucket, Prefix: strings.TrimSuffix(binding.RemotePrefix, "/"), PathStyle: binding.Destination.PathStyle}}
		request := requestAtPoint(restore.SQLiteRequest{OperationID: fmt.Sprintf("archive-retention-%s-%s-%d", p.ArchiveID, database.Binding.DatabaseID, time.Now().UnixNano()), Budget: 3 * time.Minute}, *coverage.Point)
		request.Coverage = &restore.CoverageEvidence{Source: request.Source, CoveredThrough: coverage.WritersQuiescedAt}
		for _, invariant := range database.Invariants {
			request.Invariants = append(request.Invariants, restore.Invariant{Kind: restore.InvariantKind(invariant.Kind), Table: invariant.Table, Column: invariant.Column, Count: invariant.Count, Minimum: invariant.Minimum, Maximum: invariant.Maximum})
		}
		if err := ensurePrivateChild(h.stateRoot, "archive-restores"); err != nil {
			return out, err
		}
		engine := h.archiveRestoreEngine(database, remote, credentials)
		receipt, err := restore.VerifySQLite(ctx, &engine, request)
		if err != nil || receipt.OperationID != request.OperationID || !archiveRecoveryReceiptMatches(m.Coverage[i], receipt, time.Now().UTC()) || len(database.Invariants) > 0 && receipt.InvariantCheck != "passed" {
			return out, archive.ErrRefused
		}
		out.Receipts = append(out.Receipts, receipt)
	}
	out.ArchiveID = p.ArchiveID
	out.VerifiedAt = time.Now().UTC()
	out.RetainUntil = d.RetainUntil
	out.ValidUntil = out.VerifiedAt.Add(archive.MaxEvidenceAge)
	if validUntil.Before(out.ValidUntil) {
		out.ValidUntil = validUntil
	}
	admission, err := h.ArchiveMaintenanceAdmission(ctx, p, d)
	out.PolicyHash, out.TargetHash = admission.PolicyHash, admission.TargetHash
	if err != nil || !archive.RetentionProofMatches(out, p, d, m, time.Now().UTC()) {
		return archive.RetentionProof{}, archive.ErrRefused
	}
	return out, nil
}

var _ archive.MaintenanceHost = archiveHost{}

func (h archiveHost) ArchiveCredentialHealth(ctx context.Context, p archive.Plan) ([]archive.CredentialHealth, error) {
	var out []archive.CredentialHealth
	files := backupcredentials.Files{Root: filepath.Join(h.stateRoot, "credentials")}
	now := time.Now().UTC()
	for _, variant := range p.Candidate.Databases {
		database, sqlite := variant.SQLite()
		if !sqlite {
			return nil, archive.ErrRefused
		}
		if !database.Written {
			continue
		}
		for _, binding := range database.Epochs {
			health := archive.CredentialHealth{BindingID: binding.BindingID, EpochID: binding.EpochID}
			renewed, err := h.service.Store.ReadArchiveCredentialReceipt(ctx, p.ArchiveID, binding.BindingID, binding.EpochID)
			ref, version := "", uint64(0)
			if errors.Is(err, sql.ErrNoRows) {
				original, err := h.service.Store.CredentialReceipt(ctx, binding.BindingID, binding.CredentialVersion)
				if err != nil || original.EpochID != binding.EpochID || original.CredentialRef != binding.Destination.CredentialRef || original.Destination != binding.Destination.Reference {
					health.Missing = true
				} else {
					health.ReceivedAt, health.ExpiresAt = original.ReceivedAt, original.ExpiresAt
					ref, version = original.CredentialRef, original.Version
				}
			} else if err != nil {
				health.Missing = true
			} else {
				sealed, err := files.ArchiveReceipt(renewed.Plan.Scope.FileReference(), renewed.Plan.Version)
				if err != nil || !reflect.DeepEqual(sealed, renewed) {
					health.Missing = true
				} else {
					health.ReceivedAt, health.ExpiresAt = renewed.ReceivedAt, renewed.Plan.ExpiresAt
					ref, version = renewed.Plan.Scope.FileReference(), renewed.Plan.Version
				}
			}
			if !health.Missing {
				packet, err := files.Read(ref, version)
				if err != nil {
					health.Missing = true
				}
				packet.Clear()
			}
			if !health.ReceivedAt.IsZero() && now.After(health.ReceivedAt) {
				health.AgeSeconds = int64(now.Sub(health.ReceivedAt) / time.Second)
			}
			if health.ExpiresAt != nil {
				health.Expired = !now.Before(*health.ExpiresAt)
				health.RenewalNeeded = !now.Add(3 * time.Minute).Before(*health.ExpiresAt)
			}
			health.RenewalNeeded = health.RenewalNeeded || health.Missing
			out = append(out, health)
		}
	}
	return out, nil
}
