package store

import (
	"context"
	"database/sql"

	"github.com/ShaulLavo/brine/internal/ops"
)

// A terminal source remains immutable, so a direct-child fence cannot see an
// active/successful grandchild. Fence the whole receipt family in the creation
// transaction, including branches reached through older source IDs.
func resolutionFamilyAvailable(ctx context.Context, tx *sql.Tx, source Operation) error {
	current := source
	seen := map[string]bool{}
	root := ""
	for depth := 0; depth < 64; depth++ {
		if seen[current.ID] || current.State != ops.RecoveryRequired || current.PlanID != source.PlanID || current.App != source.App || current.SecretRef != source.SecretRef {
			return ErrConflict
		}
		seen[current.ID] = true
		if current.Kind == ops.Deploy || current.Kind == ops.SecretSet {
			root = current.ID
			break
		}
		if current.Kind != ops.Resolve {
			return ErrConflict
		}
		parent, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,?),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", "", current.RecoveryOf))
		if err != nil {
			return err
		}
		current = parent
	}
	if root == "" {
		return ErrConflict
	}
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE family(id,state,depth) AS (
  SELECT id,state,0 FROM operations WHERE id=?
  UNION ALL
  SELECT o.id,o.state,f.depth+1 FROM operations o JOIN family f ON o.recovery_of=f.id WHERE f.depth<64
 ) SELECT state,depth FROM family LIMIT 4097`, root)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var state ops.State
		var depth int
		if err = rows.Scan(&state, &depth); err != nil {
			return err
		}
		count++
		if count > 4096 || depth >= 64 || state != ops.Failed && state != ops.RecoveryRequired {
			return ErrConflict
		}
	}
	return rows.Err()
}
