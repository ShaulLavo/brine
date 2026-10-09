package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
)

// InventoryState reads ownership, history and retirement in one transaction.
// A retained release is not a head, and a retirement intent is not a receipt.
func (s *Store) InventoryState(ctx context.Context) (target.ControlInventory, error) {
	result := target.ControlInventory{Apps: []target.ControlApp{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM releases)+(SELECT COUNT(*) FROM app_removals)").Scan(&result.Generation); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT app,EXISTS(SELECT 1 FROM release_heads h WHERE h.app=r.app) FROM releases r GROUP BY app`)
	if err != nil {
		return result, err
	}
	apps := map[string]target.ControlApp{}
	for rows.Next() {
		var name string
		var head bool
		if err = rows.Scan(&name, &head); err != nil {
			rows.Close()
			return result, err
		}
		status := target.Unknown
		if head {
			status = target.KnownStatus
		}
		apps[name] = target.ControlApp{Name: name, Status: status}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT app,release_id,operation_id,plan_id FROM app_removals ORDER BY app,operation_id")
	if err != nil {
		return result, err
	}
	type receipt struct{ app, release, operation, plan string }
	receipts := []receipt{}
	for rows.Next() {
		var r receipt
		if err = rows.Scan(&r.app, &r.release, &r.operation, &r.plan); err != nil {
			rows.Close()
			return result, err
		}
		receipts = append(receipts, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	for _, receipt := range receipts {
		r, err := readRelease(ctx, tx, receipt.app, receipt.release)
		if err != nil {
			return result, err
		}
		p, _, err := loadPlan(ctx, tx, receipt.plan)
		if err != nil {
			return result, err
		}
		if p.App != receipt.app || p.Lifecycle != plan.RemoveApp || p.Removal == nil || p.Removal.ReleaseID != r.ID {
			return result, &IntegrityError{}
		}
		app := apps[receipt.app]
		if app.Status != target.KnownStatus {
			settled, err := removalSettled(ctx, tx, receipt.operation, receipt.app, receipt.plan)
			if err != nil {
				return result, err
			}
			if settled {
				app.Status = target.Absent
			}
			app.RetiredPorts = append(app.RetiredPorts, r.HostPort)
			apps[receipt.app] = app
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT o.id,o.app,o.plan_id FROM operations o JOIN plans p ON p.id=o.plan_id WHERE json_extract(p.canonical,'$.lifecycle')='remove_app' AND o.state NOT IN ('succeeded','failed','rolled_back')`)
	if err != nil {
		return result, err
	}
	type pending struct{ id, app, plan string }
	pendingOps := []pending{}
	for rows.Next() {
		var op pending
		if err = rows.Scan(&op.id, &op.app, &op.plan); err != nil {
			rows.Close()
			return result, err
		}
		pendingOps = append(pendingOps, op)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	for _, op := range pendingOps {
		settled, err := removalSettled(ctx, tx, op.id, op.app, op.plan)
		if err != nil {
			return result, err
		}
		if !settled {
			app := apps[op.app]
			app.Name, app.Status = op.app, target.Unknown
			apps[op.app] = app
		}
	}
	for _, app := range apps {
		result.Apps = append(result.Apps, app)
	}
	sort.Slice(result.Apps, func(i, j int) bool { return result.Apps[i].Name < result.Apps[j].Name })
	return result, tx.Commit()
}

func removalSettled(ctx context.Context, tx *sql.Tx, id, app, planID string) (bool, error) {
	var settled bool
	err := tx.QueryRowContext(ctx, `WITH RECURSIVE family(id,state,depth) AS (
 SELECT id,state,0 FROM operations WHERE id=? AND app=? AND plan_id=? AND kind IN ('deploy','resolve')
 UNION ALL
 SELECT o.id,o.state,f.depth+1 FROM operations o JOIN family f ON o.recovery_of=f.id
 WHERE f.depth<64 AND o.app=? AND o.plan_id=? AND o.kind='resolve'
 ) SELECT EXISTS(SELECT 1 FROM family WHERE state='succeeded')`, id, app, planID, app, planID).Scan(&settled)
	return settled, err
}

func ReadInventoryState(ctx context.Context, stateDir string) (target.ControlInventory, error) {
	if _, err := os.Lstat(filepath.Join(stateDir, "control.db")); os.IsNotExist(err) {
		_, err = ReadGeneration(ctx, stateDir)
		return target.ControlInventory{Apps: []target.ControlApp{}}, err
	} else if err != nil {
		return target.ControlInventory{}, err
	}
	s, err := OpenReadOnly(ctx, stateDir)
	if err != nil {
		return target.ControlInventory{}, err
	}
	defer s.Close()
	return s.InventoryState(ctx)
}
