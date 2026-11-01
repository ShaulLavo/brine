package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
)

type UDPPreparation = string

const (
	UDPUnprepared UDPPreparation = "unprepared"
	UDPPrepared   UDPPreparation = "prepared"
	UDPCommitted  UDPPreparation = "committed"
	UDPReverted   UDPPreparation = "reverted"
	UDPUnresolved UDPPreparation = "unresolved"
)

type udpRow struct {
	plan.UDPReservation
	creator, releasedBy string
}
type udpTransition struct {
	p                   plan.Plan
	family              []string
	rows                []udpRow
	claim, retire, keep *udpRow
}

func udpFamily(ctx context.Context, tx *sql.Tx, id, planID string) ([]string, error) {
	family := []string{}
	for range 64 {
		if id == "" || slices.Contains(family, id) {
			return nil, ErrConflict
		}
		var kind ops.Kind
		var stored, parent string
		err := tx.QueryRowContext(ctx, "SELECT kind,COALESCE(plan_id,''),recovery_of FROM operations WHERE id=?", id).Scan(&kind, &stored, &parent)
		if err != nil {
			return nil, err
		}
		if stored != planID || kind != ops.Deploy && kind != ops.Resolve {
			return nil, ErrConflict
		}
		family = append(family, id)
		if kind == ops.Deploy {
			return family, nil
		}
		id = parent
	}
	return nil, ErrConflict
}

func readUDPRows(ctx context.Context, tx *sql.Tx, app string) ([]udpRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,app,incarnation,family,bind_address,host_port,state,COALESCE(owner,''),created_by,COALESCE(released_by,''),canonical FROM udp_reservations WHERE app=? ORDER BY id`, app)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []udpRow{}
	for rows.Next() {
		var r udpRow
		var raw []byte
		if err := rows.Scan(&r.ID, &r.App, &r.Incarnation, &r.Endpoint.Family, &r.Endpoint.BindAddress, &r.Endpoint.HostPort, &r.State, &r.Owner, &r.creator, &r.releasedBy, &raw); err != nil {
			return nil, err
		}
		var identity plan.UDPReservation
		if len(raw) > 4096 || json.Unmarshal(raw, &identity) != nil || identity.ID != r.ID || identity.App != r.App || identity.Incarnation != r.Incarnation || identity.Endpoint != r.Endpoint || r.Endpoint.Validate() != nil || !digestPattern.MatchString(r.Incarnation) {
			return nil, &IntegrityError{}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func udpIntent(ctx context.Context, tx *sql.Tx, op string, p plan.Plan) (udpTransition, error) {
	t := udpTransition{p: p}
	stored, desired, err := loadPlan(ctx, tx, p.Hash)
	if err != nil {
		return t, err
	}
	if err := plan.ValidateUDPExecution(stored, desired); err != nil {
		return t, ErrInvalid
	}
	expected, err := stored.CanonicalBytes()
	if err != nil {
		return t, err
	}
	supplied, err := p.CanonicalBytes()
	if err != nil || !bytes.Equal(expected, supplied) {
		return t, ErrConflict
	}
	t.family, err = udpFamily(ctx, tx, op, p.Hash)
	if err != nil {
		return t, err
	}
	t.rows, err = readUDPRows(ctx, tx, p.App)
	if err != nil {
		return t, err
	}
	for i := range t.rows {
		row := &t.rows[i]
		if err := t.classify(row); err != nil {
			return t, err
		}
	}
	return t, nil
}

func (t *udpTransition) classify(r *udpRow) error {
	u := t.p.UDP
	if u == nil {
		if r.State != "released" {
			return ErrConflict
		}
		return nil
	}
	if u.Retire != nil && r.ID == u.Retire.ID {
		if t.retire != nil || r.Endpoint != u.Retire.Endpoint || r.Incarnation != t.p.ResolvedIncarnation() {
			return ErrConflict
		}
		t.retire = r
		return nil
	}
	if u.Keep != "" && r.ID == u.Keep {
		if t.keep != nil || u.Next == nil || r.Endpoint != u.Next.Endpoint || r.Incarnation != t.p.ResolvedIncarnation() {
			return ErrConflict
		}
		t.keep = r
		return nil
	}
	if u.Next != nil && u.Keep == "" && r.ID == "udp-"+r.creator && slices.Contains(t.family, r.creator) {
		if t.claim != nil || r.Endpoint != u.Next.Endpoint || r.Incarnation != t.p.ResolvedIncarnation() {
			return ErrConflict
		}
		t.claim = r
		return nil
	}
	if r.State != "released" || slices.Contains(t.family, r.Owner) {
		return ErrConflict
	}
	return nil
}

func (t udpTransition) state() UDPPreparation {
	u := t.p.UDP
	if u == nil {
		return UDPUnprepared
	}
	if u.Keep != "" {
		if t.keep == nil || t.keep.State != "active" || t.keep.Owner != "" || u.Retire != nil {
			return UDPUnresolved
		}
		return UDPUnprepared
	}
	if u.Retire != nil && t.retire == nil {
		return UDPUnresolved
	}
	claimed := u.Next != nil
	if (!claimed || t.claim == nil) && (t.retire == nil || t.retire.State == "active") {
		return UDPUnprepared
	}
	if (!claimed || t.held(t.claim, "staged")) && (t.retire == nil || t.held(t.retire, "releasing")) {
		return UDPPrepared
	}
	if (!claimed || t.claim != nil && t.claim.State == "active" && t.claim.Owner == "") && (t.retire == nil || t.released(t.retire)) {
		return UDPCommitted
	}
	if (!claimed || t.released(t.claim)) && (t.retire == nil || t.retire.State == "active" && t.retire.Owner == "") {
		return UDPReverted
	}
	return UDPUnresolved
}
func (t udpTransition) held(r *udpRow, state string) bool {
	return r != nil && r.State == state && slices.Contains(t.family, r.Owner)
}
func (t udpTransition) released(r *udpRow) bool {
	return r != nil && r.State == "released" && slices.Contains(t.family, r.releasedBy)
}

func (s *Store) UDPPreparation(ctx context.Context, op string, p plan.Plan) (UDPPreparation, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return UDPUnresolved, err
	}
	defer func() { _ = tx.Rollback() }()
	t, err := udpIntent(ctx, tx, op, p)
	if err != nil {
		return UDPUnresolved, err
	}
	state := t.state()
	var committed bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM releases WHERE app=? AND plan_id=? AND id IN (SELECT id FROM operations WHERE plan_id=?))", p.App, p.Hash, p.Hash).Scan(&committed); err != nil {
		return UDPUnresolved, err
	}
	if committed && (state == UDPUnprepared && p.UDP != nil && p.UDP.Keep != "" || state == UDPCommitted) {
		state = UDPCommitted
	}
	return state, tx.Commit()
}

func (s *Store) PrepareUDP(ctx context.Context, op string, p plan.Plan) error {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	t, err := udpIntent(ctx, tx, op, p)
	if err != nil {
		return err
	}
	if t.state() == UDPPrepared {
		return tx.Commit()
	}
	var terminal ops.State
	if err := tx.QueryRowContext(ctx, "SELECT state FROM operations WHERE id=?", op).Scan(&terminal); err != nil {
		return err
	}
	if terminal.IsTerminal() {
		return ErrConflict
	}
	if err := verifyUDPCommit(ctx, tx, op, p, false); err != nil {
		return err
	}
	if p.UDP == nil || t.state() != UDPUnprepared {
		return ErrConflict
	}
	if t.retire != nil {
		if err := udpUpdate(ctx, tx, t.retire.ID, "active", "", "releasing", op, ""); err != nil {
			return err
		}
	}
	if p.UDP.Next != nil && p.UDP.Keep == "" {
		r := plan.UDPReservation{ID: "udp-" + op, App: p.App, Incarnation: p.ResolvedIncarnation(), State: "staged", Owner: op, Endpoint: p.UDP.Next.Endpoint}
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO udp_reservations(id,app,incarnation,family,bind_address,host_port,state,owner,created_by,canonical) VALUES(?,?,?,?,?,?,'staged',?,?,?)`, r.ID, r.App, r.Incarnation, r.Endpoint.Family, r.Endpoint.BindAddress, r.Endpoint.HostPort, op, op, raw)
		if err != nil {
			return errors.Join(ErrConflict, err)
		}
	}
	return tx.Commit()
}

func udpUpdate(ctx context.Context, tx *sql.Tx, id, before, owner, after, nextOwner, releasedBy string) error {
	r, err := tx.ExecContext(ctx, `UPDATE udp_reservations SET state=?,owner=NULLIF(?,''),released_by=NULLIF(?,'') WHERE id=? AND state=? AND COALESCE(owner,'')=?`, after, nextOwner, releasedBy, id, before, owner)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) RevertUDP(ctx context.Context, op string, p plan.Plan) error {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	t, err := udpIntent(ctx, tx, op, p)
	if err != nil {
		return err
	}
	state := t.state()
	if state == UDPReverted || state == UDPUnprepared {
		return tx.Commit()
	}
	if state != UDPPrepared {
		return ErrConflict
	}
	if t.claim != nil {
		if err := udpUpdate(ctx, tx, t.claim.ID, "staged", t.claim.Owner, "released", "", op); err != nil {
			return err
		}
	}
	if t.retire != nil {
		if err := udpUpdate(ctx, tx, t.retire.ID, "releasing", t.retire.Owner, "active", "", ""); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func settleUDP(ctx context.Context, tx *sql.Tx, op string, p plan.Plan) error {
	t, err := udpIntent(ctx, tx, op, p)
	if err != nil {
		return err
	}
	if p.UDP == nil {
		return nil
	}
	if p.UDP.Keep != "" {
		if t.state() != UDPUnprepared {
			return ErrConflict
		}
		return nil
	}
	if t.state() != UDPPrepared {
		return ErrConflict
	}
	if t.retire != nil {
		if err := udpUpdate(ctx, tx, t.retire.ID, "releasing", t.retire.Owner, "released", "", op); err != nil {
			return err
		}
	}
	if t.claim != nil {
		return udpUpdate(ctx, tx, t.claim.ID, "staged", t.claim.Owner, "active", "", "")
	}
	return nil
}

func (s *Store) UDPOperationFamily(ctx context.Context, id string, p plan.Plan) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	family, err := udpFamily(ctx, tx, id, p.Hash)
	if err != nil {
		return nil, err
	}
	return family, tx.Commit()
}

func verifyUDPCommit(ctx context.Context, tx *sql.Tx, op string, p plan.Plan, replay bool) error {
	if p.UDP == nil {
		var held bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM udp_reservations WHERE app=? AND state<>'released')", p.App).Scan(&held); err != nil {
			return err
		}
		if held {
			return ErrConflict
		}
		return nil
	}
	t, err := udpIntent(ctx, tx, op, p)
	if err != nil {
		return err
	}
	if replay {
		if t.state() != UDPCommitted && !(p.UDP.Keep != "" && t.state() == UDPUnprepared) {
			return ErrConflict
		}
		return nil
	}
	var incarnation string
	err = tx.QueryRowContext(ctx, "SELECT incarnation_id FROM release_heads WHERE app=?", p.App).Scan(&incarnation)
	if p.Kind == plan.Create {
		if !errors.Is(err, sql.ErrNoRows) || p.UDP.Incarnation != "" || p.ResolvedIncarnation() != p.Hash {
			return ErrConflict
		}
		return nil
	}
	if err != nil || incarnation != p.ResolvedIncarnation() {
		return ErrConflict
	}
	return nil
}

func guardUDPFamilySettled(ctx context.Context, tx *sql.Tx, id string) error {
	var planID string
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(plan_id,'') FROM operations WHERE id=?", id).Scan(&planID); err != nil {
		return err
	}
	if planID == "" {
		return nil
	}
	family, err := udpFamily(ctx, tx, id, planID)
	if err != nil {
		return err
	}
	for _, owner := range family {
		var held bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM udp_reservations WHERE owner=? AND state IN ('staged','releasing'))", owner).Scan(&held); err != nil {
			return err
		}
		if held {
			return ErrConflict
		}
	}
	return nil
}
