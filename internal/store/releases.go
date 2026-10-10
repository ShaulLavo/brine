package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

func normalizeRelease(r Release) (Release, []byte, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return r, nil, ErrInvalid
	}
	if json.Unmarshal(b, &r) != nil {
		return r, nil, ErrInvalid
	}
	slices.SortFunc(r.Units, func(a, b target.Unit) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(r.Secrets, func(a, b plan.SecretBinding) int { return strings.Compare(a.Environment, b.Environment) })
	b, err = json.Marshal(r)
	if err != nil || len(b) > MaxPlanBytes {
		return r, nil, ErrInvalid
	}
	return r, b, nil
}
func verifyRelease(app string, r Release, p plan.Plan, d policy.Desired) error {
	if r.ID == "" || len(r.ID) > 256 || app != p.App || string(d.Name) != app || r.PlanID != p.Hash || !reflect.DeepEqual(r.Image, p.Image) || r.HostPort != p.HostPort || r.Units == nil || len(r.Units) == 0 || r.Secrets == nil || r.CaddyFile.Name != app+".caddy" || !digestPattern.MatchString(r.CaddyFile.Hash) {
		return ErrInvalid
	}
	secrets := slices.Clone(p.Secrets)
	slices.SortFunc(secrets, func(a, b plan.SecretBinding) int { return strings.Compare(a.Environment, b.Environment) })
	if !reflect.DeepEqual(r.Secrets, secrets) {
		return ErrInvalid
	}
	found := false
	for i, u := range r.Units {
		if u.Name == app+".container" {
			found = true
		}
		if u.Name == "" || !digestPattern.MatchString(u.Hash) || (i > 0 && r.Units[i-1].Name == u.Name) {
			return ErrInvalid
		}
	}
	if !found {
		return ErrInvalid
	}
	return nil
}
func readRelease(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, app, id string) (Release, error) {
	var r Release
	var raw []byte
	var sum, planID string
	err := q.QueryRowContext(ctx, "SELECT content,content_hash,plan_id FROM releases WHERE app=? AND id=?", app, id).Scan(&raw, &sum, &planID)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if len(raw) > MaxPlanBytes || digest(raw) != sum || json.Unmarshal(raw, &r) != nil || r.ID != id || r.PlanID != planID {
		return Release{}, &IntegrityError{}
	}
	canonical, encoded, err := normalizeRelease(r)
	if err != nil || !bytes.Equal(raw, encoded) {
		return Release{}, &IntegrityError{}
	}
	p, d, err := loadPlan(ctx, q, r.PlanID)
	if err != nil {
		return Release{}, err
	}
	if verifyRelease(app, canonical, p, d) != nil {
		return Release{}, &IntegrityError{}
	}
	return canonical, nil
}
func (s *Store) CurrentRelease(ctx context.Context, app string) (Release, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Release{}, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, "SELECT current_id FROM release_heads WHERE app=?", app).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, ErrNotFound
	}
	if err != nil {
		return Release{}, err
	}
	r, err := readRelease(ctx, tx, app, id)
	if err != nil {
		return Release{}, err
	}
	return r, tx.Commit()
}
func (s *Store) PreviousRelease(ctx context.Context, app string) (Release, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Release{}, err
	}
	defer tx.Rollback()
	var id sql.NullString
	err = tx.QueryRowContext(ctx, "SELECT previous_id FROM release_heads WHERE app=?", app).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !id.Valid {
		return Release{}, ErrNotFound
	}
	if err != nil {
		return Release{}, err
	}
	r, err := readRelease(ctx, tx, app, id.String)
	if err != nil {
		return Release{}, err
	}
	return r, tx.Commit()
}

// CommitRelease records an already health-checked release, not a health claim.
// Callers hold the host lock through external commit and this local transaction.
// Replaying an identical release is a no-op and never rotates the previous head.
func (s *Store) CommitRelease(ctx context.Context, app string, r Release) error {
	r, raw, err := normalizeRelease(r)
	if err != nil {
		return err
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, d, err := loadPlan(ctx, tx, r.PlanID)
	if err != nil {
		return err
	}
	if err = verifyRelease(app, r, p, d); err != nil {
		return err
	}
	var old []byte
	err = tx.QueryRowContext(ctx, "SELECT content FROM releases WHERE app=? AND id=?", app, r.ID).Scan(&old)
	if err == nil {
		if !bytes.Equal(raw, old) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO releases VALUES(?,?,?,?,?,?)", app, r.ID, r.PlanID, raw, digest(raw), timestamp()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO release_heads(app,current_id,previous_id) VALUES(?,?,NULL) ON CONFLICT(app) DO UPDATE SET previous_id=current_id,current_id=excluded.current_id`, app, r.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) LoadBrineState(ctx context.Context, identity target.Identity, generation uint64) (plan.BrineState, error) {
	result := plan.BrineState{Target: identity, Generation: generation, Releases: []plan.CurrentRelease{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT app,current_id FROM release_heads ORDER BY app")
	if err != nil {
		return result, err
	}
	type head struct{ app, id string }
	heads := []head{}
	for rows.Next() {
		var h head
		if err = rows.Scan(&h.app, &h.id); err != nil {
			rows.Close()
			return result, err
		}
		heads = append(heads, h)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	for _, h := range heads {
		r, err := readRelease(ctx, tx, h.app, h.id)
		if err != nil {
			return result, err
		}
		p, d, err := loadPlan(ctx, tx, r.PlanID)
		if err != nil {
			return result, err
		}
		if p.Target != identity {
			return result, &IntegrityError{}
		}
		result.Releases = append(result.Releases, plan.CurrentRelease{App: h.app, ID: r.ID, Desired: d, Image: r.Image, HostPort: r.HostPort, Secrets: r.Secrets, Units: r.Units, CaddyFile: r.CaddyFile})
	}
	return result, tx.Commit()
}

// ReleaseByID reads immutable history scoped to one app, never another app's ID.
func (s *Store) ReleaseByID(ctx context.Context, app, id string) (Release, error) {
	return readRelease(ctx, s.db, app, id)
}

// Generation advances once for each immutable committed release. Replaying a
// commit does not advance it, and plans and queued jobs do not affect it.
func (s *Store) Generation(ctx context.Context) (uint64, error) {
	var n uint64
	err := s.db.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM releases)+(SELECT COUNT(*) FROM app_removals)").Scan(&n)
	return n, err
}
