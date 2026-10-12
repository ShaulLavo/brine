//go:build linux

package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

func nativeHistoricalRestoreTask(service Service, runtime nativeBackupRuntime) jobs.TaskHandler {
	return func(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
		claim, err := service.Store.ClaimNativeHistoricalRestore(ctx, op.ID)
		if err != nil {
			return nil, err
		}
		defer func() { _ = claim.Close() }()
		bundle, err := claim.Bundle(ctx)
		pol, policyErr := service.Policy.Load(ctx)
		if err != nil || policyErr != nil || pol.Hash() != "sha256:"+bundle.Point.PolicySHA256 {
			return nil, result.New(result.PolicyRefused, nil)
		}
		ctx, cancel, err := nativeTransportContext(ctx, bundle.Material.Scope)
		if err != nil {
			return nil, err
		}
		defer cancel()
		client, repository, err := runtime.repositoryMaterial(ctx, bundle.Material, claim, "")
		if err != nil {
			return nil, err
		}
		parent, path, err := newNativeHistoricalRestoreParent(runtime.stateRoot)
		if err != nil {
			return nil, err
		}
		defer func() { _ = parent.Close() }()
		extracted, err := client.RestoreExact(ctx, repository, path, parent, "download", bundle.Transport, bundle.Manifest)
		if err != nil {
			return nil, err
		}
		defer func() { _ = extracted.Root().Close() }()
		_, _, err = runtime.verifyExtracted(ctx, extracted, parent, path, bundle.Checkpoint, bundle.Selection, bundle.SourceEvidence, bundle.Schema, bundle.ConfirmedBound, bundle.FinalPairs)
		if err != nil {
			return nil, err
		}
		current, err := claim.Bundle(ctx)
		if err != nil || current.Point.PointSHA256 != bundle.Point.PointSHA256 {
			return nil, result.New(result.PolicyRefused, nil)
		}
		return json.Marshal(nativecapture.TaskReceipt{Kind: string(ops.NativeRestoreTest), OperationID: op.ID, BindingID: string(bundle.Point.BindingID), EpochID: string(bundle.Point.EpochID), PointID: bundle.Point.PointID, PointSHA256: bundle.Point.PointSHA256, Outcome: "verified"})
	}
}

func newNativeHistoricalRestoreParent(stateRoot string) (*os.Root, string, error) {
	root, err := engineOpenRoot(stateRoot)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = root.Close() }()
	if err := engineMakePrivateDirectories(root, "native-restore-tests"); err != nil {
		return nil, "", err
	}
	nonce, err := data.NewID()
	if err != nil {
		return nil, "", err
	}
	name := filepath.Join("native-restore-tests", nonce)
	if err := root.Mkdir(name, 0700); err != nil {
		return nil, "", err
	}
	parent, err := root.OpenRoot(name)
	if err != nil {
		return nil, "", err
	}
	return parent, filepath.Join(stateRoot, name), nil
}
