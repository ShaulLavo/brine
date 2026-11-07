package archive

import (
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restore"
)

func removalLTXFixture(t *testing.T) (Plan, SQLiteDatabase, SQLiteCoverage, time.Time) {
	t.Helper()
	p, d, c, now := removalFixture(t)
	point := c.Point
	barrier := data.RestoreUploadBarrier{BindingID: point.BindingID, EpochID: point.EpochID, TXID: 7, ReplicaTXID: 8, ObservedAt: point.RecordedAt}
	point.Kind, point.Snapshot = data.RestorePointLTX, nil
	point.LTX = &data.RestoreLTXPoint{TXID: 7, Barrier: barrier}
	source := restore.SQLiteSource{Kind: restore.LitestreamLTX, LTX: &restore.LTXSource{BindingID: string(point.BindingID), Epoch: string(point.EpochID), TXID: 7, Barrier: &restore.BarrierReceipt{BindingID: string(point.BindingID), Epoch: string(point.EpochID), TXID: 7, ReplicaTXID: 8, ObservedAt: point.RecordedAt, Succeeded: true}}}
	c.Receipt.Source, c.Receipt.Coverage.Source = source, source
	c.Receipt.ToolVersion = restore.LitestreamVersion
	c.Receipt.RequestedTXID, c.Receipt.RecoveredTXID = 7, 7
	c.Receipt.PositionEvidence = "pinned_cli_exact_plan_and_successful_restore"
	return p, d, c, now
}

func TestRemovalPreparedLTXSourceJoins(t *testing.T) {
	p, d, c, now := removalLTXFixture(t)
	requirePreparedAdmission(t, p, d, c, now, true)
	c.Receipt.Source.LTX.Barrier.ReplicaTXID++
	requirePreparedAdmission(t, p, d, c, now, false)
	p, d, c, now = removalLTXFixture(t)
	c.Point.LTX.Barrier.ObservedAt = c.Prepared.Ref.Cutoff.Add(-time.Nanosecond)
	c.Receipt.Source.LTX.Barrier.ObservedAt = c.Point.LTX.Barrier.ObservedAt
	requirePreparedAdmission(t, p, d, c, now, false)
}

func TestRemovalPreparedAdmissionDoesNotRenewPointExpiry(t *testing.T) {
	p, d, c, claim := removalFixture(t)
	c.Prepared.Ref.ExpiresAt = claim.Add(time.Second)
	p.Removal.Points[0] = c.Prepared.Ref
	p.Removal.ExpiresAt = c.Prepared.Ref.ExpiresAt
	p.ID, _ = p.Fingerprint()
	requirePreparedAdmission(t, p, d, c, claim.Add(MaxEvidenceAge), true)
	c.Prepared.ClaimedAt = c.Prepared.Ref.ExpiresAt
	requirePreparedAdmission(t, p, d, c, c.Prepared.ClaimedAt, false)
}
