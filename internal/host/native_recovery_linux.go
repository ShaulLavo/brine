//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strconv"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

type nativeRecoveryOperations struct {
	service Service
	jobs    jobs.Service
}

func (o nativeRecoveryOperations) SetRecovery(ctx context.Context, args dispatch.NativeRecoveryArgs) (jobs.Accepted, error) {
	if !args.Valid() {
		return jobs.Accepted{}, result.New(result.InvalidUsage, nil)
	}
	p, desired, err := o.service.Store.LoadPlan(ctx, args.DeploymentPlanID)
	if err != nil || p.Managed == nil || desired.SpacetimeDB == nil {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	pol, err := o.service.Policy.Load(ctx)
	if err != nil || pol.Hash() != desired.PolicyHash {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	password := args.Password()
	defer clear(password)
	sum := sha256.Sum256(password)
	input, err := o.service.Store.ReserveNativeRecoveryInput(ctx, args.DeploymentPlanID, o.jobs.Requester, args.IdempotencyKey, hex.EncodeToString(sum[:]))
	if err != nil {
		return jobs.Accepted{}, err
	}
	delivery, err := o.service.Store.ClaimNativeRecoveryDelivery(ctx, input.ID, o.jobs.Requester)
	if err != nil {
		return jobs.Accepted{}, err
	}
	if err = writeNativeRecoveryPassword(ctx, delivery, password); err != nil {
		_ = delivery.Close()
		return jobs.Accepted{}, err
	}
	if err = delivery.Close(); err != nil {
		return jobs.Accepted{}, err
	}
	return o.jobs.Submit(ctx, ops.Intent{Kind: ops.NativeRecoverySet, App: input.App, SecretRef: input.ID}, args.IdempotencyKey)
}

func nativeRecoveryPath(input store.NativeRecoveryInput) string {
	return filepath.Join("native-recovery", string(input.BindingID), input.Reference, "v"+strconv.FormatUint(input.Version, 10)+".secret")
}

func writeNativeRecoveryPassword(ctx context.Context, delivery *store.NativeRecoveryDelivery, password []byte) error {
	input, err := delivery.Input(ctx)
	sum := sha256.Sum256(password)
	if err != nil || len(password) < 16 || len(password) > 4096 || hex.EncodeToString(sum[:]) != input.MaterialSHA256 {
		return errEngineSource
	}
	root, err := engineOpenRoot(delivery.StateRoot())
	if err != nil {
		return errEngineSource
	}
	defer func() { _ = root.Close() }()
	path := nativeRecoveryPath(input)
	if err = engineMakePrivateDirectories(root, filepath.Dir(path)); err != nil {
		return err
	}
	if err = engineWritePrivateVersion(ctx, root, path, input.MaterialSHA256, password); err != nil {
		return err
	}
	if _, err = delivery.Input(ctx); err != nil {
		return err
	}
	return nil
}

func nativeRecoveryTask(service Service) jobs.TaskHandler {
	return func(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
		delivery, err := service.Store.ClaimNativeRecoveryDelivery(ctx, op.SecretRef, op.Requester)
		if err != nil {
			return nil, err
		}
		defer func() { _ = delivery.Close() }()
		input, err := delivery.Input(ctx)
		if err != nil {
			return nil, err
		}
		pol, err := service.Policy.Load(ctx)
		if err != nil || pol.Hash() != "sha256:"+input.PolicySHA256 {
			return nil, result.New(result.PolicyRefused, nil)
		}
		root, err := engineOpenRoot(delivery.StateRoot())
		if err != nil {
			return nil, err
		}
		defer func() { _ = root.Close() }()
		password, err := readEnginePrivateFile(ctx, root, nativeRecoveryPath(input), input.MaterialSHA256, 4096)
		clear(password)
		if err != nil {
			return nil, err
		}
		receipt, err := service.Store.RecordNativeRecoveryDelivery(ctx, delivery, op.ID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(receipt)
	}
}
