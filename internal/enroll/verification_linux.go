package enroll

import (
	"context"
	"errors"
	"fmt"
	"github.com/ShaulLavo/brine/internal/runnerstorage"
)

type verificationFile struct {
	path string
	hash string
}

func (h *host) verificationFiles(req HostRequest) ([]verificationFile, error) {
	if h.r.ID == "" || h.r.Journal.Phase != Enrolled {
		return nil, errors.New("enrollment incomplete")
	}
	key, err := PublicKey(req.DeployKey)
	if err != nil || key != h.r.Key {
		return nil, errors.New("verification deploy key differs from original enrollment")
	}
	if h.r.BinaryHash == "" || h.r.Files[binaryPath].Hash != h.r.BinaryHash {
		return nil, errors.New("verification binary differs from original enrollment intent")
	}
	files := []verificationFile{{binaryPath, h.r.BinaryHash}, {sshIdentityPath, hash(req.IdentityKey)}, {sshKeyPath, hash(h.keyFile())}, {runnerstorage.Path, hash([]byte(runnerstorage.Config))}}
	for _, file := range files {
		if h.r.Files[file.path].Hash != file.hash {
			return nil, fmt.Errorf("verification file differs from original enrollment intent: %s", file.path)
		}
	}
	return files, nil
}

func (h *host) verifyEnrollment(ctx context.Context, req HostRequest) error {
	files, err := h.verificationFiles(req)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := protectedParents(file.path); err != nil {
			return err
		}
		ok, err := h.fileMatches(file.path, file.hash)
		if err != nil || !ok {
			return errors.Join(err, fmt.Errorf("original enrollment verification refused: %s", file.path))
		}
	}
	facts, err := h.facts(ctx, req.IdentityKey)
	if err != nil {
		return err
	}
	plan, err := MakePlan(facts)
	if err != nil || len(plan.MissingPackages) != 0 {
		return errors.Join(err, errors.New("verification requires all runtime packages"))
	}
	if err := h.qualifyRunnerStorage(ctx); err != nil {
		return err
	}
	if err := h.checkRootlessHelpers(ctx); err != nil {
		return err
	}
	for _, asset := range []struct {
		name  string
		check func(context.Context) (bool, error)
	}{{"litestream", h.checkLitestream}, {"restic", h.checkRestic}, {"native-reader", h.checkNativeReader}, {"workspace-observer", h.checkWorkspaceObserver}} {
		ok, err := asset.check(ctx)
		if err != nil || !ok {
			return errors.Join(err, fmt.Errorf("enrollment asset verification refused: %s", asset.name))
		}
	}
	return nil
}
