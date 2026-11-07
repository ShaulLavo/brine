//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/store"
)

func (h archiveHost) CheckRemovalRecovery(ctx context.Context, p archive.Plan, o archive.Operation, m archive.Manifest, task string) ([]restore.SQLiteReceipt, error) {
	if p.Removal == nil || task == "" {
		return nil, archive.ErrRefused
	}
	return h.checkArchiveRecovery(ctx, p, o, m, task)
}

func (h archiveHost) RecordRemovalRecovery(ctx context.Context, task string, o archive.Operation, receipts []restore.SQLiteReceipt, hold archive.SourceHold) error {
	p, err := h.service.Store.LoadArchivePlan(ctx, o.PlanID)
	if err != nil || p.Removal == nil {
		return archive.ErrRefused
	}
	evidence := make([]store.SQLiteRemovalRecoveryEvidence, 0, len(receipts))
	index := 0
	for _, d := range p.Candidate.Databases {
		if !d.Written() {
			continue
		}
		if index >= len(receipts) {
			return archive.ErrRefused
		}
		evidence = append(evidence, store.SQLiteRemovalRecoveryEvidence{DatabaseID: d.Reference().DatabaseID, Verification: receipts[index]})
		index++
	}
	if index != len(receipts) {
		return archive.ErrRefused
	}
	return h.service.Store.RecordSQLiteRemovalRecovery(ctx, task, o, evidence, hold)
}

var _ archive.RemovalRecoveryHost = archiveHost{}

// archiveRestoreTesting injects a bounded local provider and CLI for isolated
// restore tests. Normal constructors leave it nil and use the pinned engine.
type archiveRestoreTesting struct {
	cli      restore.CLI
	endpoint string
}
