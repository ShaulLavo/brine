package archive

import (
	"context"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

// Removal binds a normal-removal plan to store-selected preparation points.
// Bounds are explicit limits, not a grant of removal permission.
type Removal struct {
	Bounds    data.InitializationBounds `json:"bounds"`
	Points    []PreparedRef             `json:"points"`
	ExpiresAt time.Time                 `json:"expires_at"`
}

type PreparedRef struct {
	DatabaseID        data.DatabaseID      `json:"database_id"`
	PreparationID     string               `json:"preparation_id"`
	PreparedSHA256    string               `json:"prepared_sha256"`
	SourcePointID     string               `json:"source_point_id"`
	Fence             data.QuiescenceFence `json:"fence"`
	CredentialVersion uint64               `json:"credential_version"`
	DestinationSHA256 string               `json:"destination_sha256"`
	RetentionSHA256   string               `json:"retention_sha256"`
	LocationSHA256    string               `json:"location_sha256"`
	Cutoff            time.Time            `json:"cutoff"`
	ExpiresAt         time.Time            `json:"expires_at"`
}

type PreparedCoverage struct {
	Ref       PreparedRef `json:"ref"`
	ClaimedAt time.Time   `json:"claimed_at"`
}

type RemovalJournal interface {
	ClaimRemoval(ctx context.Context, p Plan, task string, hold SourceHold) (Operation, error)
	FinishRemoval(ctx context.Context, task string, o Operation, old, next Disposition, hold SourceHold) (Disposition, RemovalReceipt, error)
}

type RemovalHost interface {
	HoldPrepared(ctx context.Context, p Plan) (SourceHold, error)
	CoverPrepared(ctx context.Context, p Plan, ref PreparedRef, operation string, claimed time.Time) (Coverage, error)
}

// SourceHold grants no authority. The store must authenticate its own registered
// aggregate and every live child capability before claim or completion.
type SourceHold interface {
	Recheck(ctx context.Context) error
	Close() error
}

func removalTime(t time.Time) bool {
	return !t.IsZero() && t.Location() == time.UTC && t.Year() >= 2000 && t.Year() <= 9999
}

func (r PreparedRef) valid() bool {
	f := r.Fence
	if !data.ValidID(string(r.DatabaseID)) || !data.ValidID(r.PreparationID) || !data.ValidID(r.SourcePointID) || !data.ValidID(string(f.ID)) || !data.ValidID(string(f.IncarnationID)) || f.DatabaseID != r.DatabaseID || f.State != data.FenceHeld || !nativeArchiveOperationID.MatchString(f.OperationID) || r.CredentialVersion == 0 || r.CredentialVersion > 1<<63-1 {
		return false
	}
	for _, hash := range []string{r.PreparedSHA256, r.DestinationSHA256, r.RetentionSHA256, r.LocationSHA256} {
		if !data.ValidCatalogHash(hash) {
			return false
		}
	}
	return removalTime(r.Cutoff) && removalTime(r.ExpiresAt) && r.Cutoff.Before(r.ExpiresAt)
}

func (r Removal) valid(p Plan) bool {
	if !r.Bounds.Valid() || !removalTime(p.CreatedAt) || !removalTime(r.ExpiresAt) || !r.ExpiresAt.After(p.CreatedAt) || r.ExpiresAt.Sub(p.CreatedAt) > 15*time.Minute || len(r.Points) > 16 {
		return false
	}
	refs := make(map[data.DatabaseID]PreparedRef, len(r.Points))
	fences := make(map[data.FenceID]bool, len(r.Points))
	for i, ref := range r.Points {
		if !ref.valid() || ref.Fence.IncarnationID != p.Candidate.Incarnation || ref.Cutoff.After(p.CreatedAt) || r.ExpiresAt.After(ref.ExpiresAt) || fences[ref.Fence.ID] || i > 0 && r.Points[i-1].DatabaseID >= ref.DatabaseID {
			return false
		}
		refs[ref.DatabaseID], fences[ref.Fence.ID] = ref, true
	}
	for _, variant := range p.Candidate.Databases {
		database, ok := variant.SQLite()
		if !ok || len(database.Epochs) != 1 {
			return false
		}
		_, present := refs[database.Binding.DatabaseID]
		if database.Written && !present {
			return false
		}
		delete(refs, database.Binding.DatabaseID)
	}
	return len(refs) == 0
}
