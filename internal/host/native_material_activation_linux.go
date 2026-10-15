//go:build linux

package host

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"lukechampine.com/blake3"
)

func (o nativeMaterialOperations) PlanMaterial(ctx context.Context, args dispatch.NativeMaterialPlanArgs) (nativematerial.ActivationPlan, error) {
	if !dispatch.ValidApp(args.App) || o.service.Store == nil || o.service.Policy == nil {
		return nativematerial.ActivationPlan{}, result.New(result.InvalidUsage, nil)
	}
	lock, err := o.service.Store.AcquireHostLock(ctx)
	if err != nil {
		return nativematerial.ActivationPlan{}, err
	}
	defer func() { _ = lock.Release() }()
	input, err := o.service.Store.ReadNativeMaterial(ctx, args.DeliveryOperationID, o.service.Requester)
	pol, perr := o.service.Policy.Load(ctx)
	if err != nil || perr != nil || input.PolicyHash != pol.Hash() {
		return nativematerial.ActivationPlan{}, result.New(result.PolicyRefused, nil)
	}
	p, err := o.service.Store.SaveNativeMaterialActivationPlan(ctx, args.App, args.DeliveryOperationID, o.service.Requester)
	if err != nil {
		return p, materialActivationError(err)
	}
	return p, nil
}
func (o nativeMaterialOperations) ActivateMaterial(ctx context.Context, args dispatch.NativeMaterialActivateArgs) (nativematerial.ActivationReceipt, error) {
	if !data.ValidID(args.PlanID) || o.service.Store == nil || o.service.Policy == nil || o.service.managedObserver == nil {
		return nativematerial.ActivationReceipt{}, result.New(result.InvalidUsage, nil)
	}
	lock, err := o.service.Store.AcquireHostLock(ctx)
	if err != nil {
		return nativematerial.ActivationReceipt{}, err
	}
	defer func() { _ = lock.Release() }()
	p, err := o.service.Store.ReadNativeMaterialActivationPlan(ctx, args.PlanID)
	pol, perr := o.service.Policy.Load(ctx)
	if err != nil || perr != nil || p.Requester != o.service.Requester || p.PolicyHash != pol.Hash() {
		return nativematerial.ActivationReceipt{}, result.New(result.PolicyRefused, nil)
	}
	receipt, err := o.service.Store.ReadNativeMaterialActivationReceipt(ctx, p.ID)
	if err == nil {
		return receipt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return receipt, result.New(result.RecoveryRequired, nil)
	}
	if !time.Now().UTC().Before(p.ExpiresAt) {
		return receipt, result.New(result.Conflict, nil)
	}
	guard, err := data.AcquireEngineLaunchGuard(ctx, o.service.managedObserver.stateRoot, p.Head.BindingID)
	if err != nil {
		return receipt, result.New(result.Conflict, nil)
	}
	defer func() { _ = guard.Release() }()
	lifetime, err := data.AcquireEngineLifetimeLock(ctx, o.service.managedObserver.stateRoot, p.Head.BindingID)
	if err != nil {
		return receipt, result.New(result.Conflict, nil)
	}
	defer func() { _ = lifetime.Release() }()
	permit, err := o.service.Store.ReadEnginePermit(ctx, p.Head.BindingID)
	if err != nil || o.service.managedObserver.stopped(ctx, permit) != nil || o.frontendStoppedForMaterial(ctx, p.App) != nil {
		return receipt, result.New(result.Conflict, nil)
	}
	candidate := permit
	candidate.Runtime.Secrets = p.Secrets
	if !candidate.Runtime.PublishingSecretsAdmit(time.Now().UTC(), time.Duration(candidate.Runtime.StartupSeconds+candidate.Runtime.StopSeconds+35)*time.Second) {
		return receipt, result.New(result.NativeMaterialLeaseExpired, nil)
	}
	material, err := readPublisherMaterial(ctx, o.service.managedObserver.stateRoot, candidate)
	if err != nil {
		return receipt, result.New(result.Conflict, nil)
	}
	defer material.clear()
	if !materialPublisherMatches(material.publisher, candidate.Runtime.Publisher) {
		return receipt, result.New(result.PolicyRefused, nil)
	}
	receipt, err = o.service.Store.ActivateNativeMaterial(ctx, p, guard, lifetime)
	if err != nil {
		return receipt, materialActivationError(err)
	}
	return receipt, nil
}
func (o nativeMaterialOperations) frontendStoppedForMaterial(ctx context.Context, app string) error {
	unit, err := systemd.ParseUnit(app + ".service")
	if err != nil {
		return errEngineSource
	}
	manager := o.service.managedObserver.units
	pending, err := manager.JobPending(ctx, unit)
	if err != nil || pending {
		return errEngineSource
	}
	props, err := manager.Show(ctx, unit)
	if managedUnitAbsent(err) {
		return nil
	}
	if err != nil || props.ActiveState != "inactive" && props.ActiveState != "failed" || props.SubState != "dead" && props.SubState != "failed" {
		return errEngineSource
	}
	return nil
}

var _ dispatch.NativeMaterialActivationOperations = nativeMaterialOperations{}

func materialActivationError(err error) error {
	switch {
	case errors.Is(err, store.ErrMaterialRequiresStop):
		return result.New(result.NativeMaterialRequiresStop, nil)
	case errors.Is(err, store.ErrMaterialLeaseExpired):
		return result.New(result.NativeMaterialLeaseExpired, nil)
	case errors.Is(err, store.ErrConflict):
		return result.New(result.Conflict, nil)
	default:
		return result.New(result.RecoveryRequired, nil)
	}
}
func materialPublisherMatches(token []byte, expected data.NativePublisherIdentity) bool {
	parts := strings.Split(string(token), ".")
	if len(parts) != 3 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	defer clear(raw)
	var claims struct {
		Identity string `json:"hex_identity"`
		Issuer   string `json:"iss"`
		Subject  string `json:"sub"`
	}
	if err != nil || json.Unmarshal(raw, &claims) != nil || claims.Identity != string(expected) || claims.Issuer == "" || claims.Subject == "" {
		return false
	}
	// SpacetimeDB 2.11.0 crates/lib/src/identity.rs, Identity::from_claims.
	hash := blake3.Sum256([]byte(claims.Issuer + "|" + claims.Subject))
	var checksumInput [28]byte
	checksumInput[0] = 0xc2
	copy(checksumInput[2:], hash[:26])
	checksum := blake3.Sum256(checksumInput[:])
	identity := [32]byte{0xc2, 0x00}
	copy(identity[2:6], checksum[:4])
	copy(identity[6:], hash[:26])
	return hex.EncodeToString(identity[:]) == string(expected)
}
