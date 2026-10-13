//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/store"
)

type nativeInitialStageWork struct {
	journal                                *store.NativeInitialStage
	workspaceID, copyID, nonce, sourceSeal string
	observed                               store.NativeInitialStageObservation
	lease                                  *publicationStageLease
	failureCode                            string
}

func beginInitialPublisherWork(ctx context.Context, w *nativePrepareWork) error {
	source, err := openEngineSource(w.permit.Reserved.Database)
	if err != nil {
		return err
	}
	sealed, err := sealOwnedEngineSource(ctx, source, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	err = errors.Join(err, source.Close())
	if err != nil {
		return err
	}
	workspace, err := data.NewID()
	if err != nil {
		return err
	}
	copyID, err := data.NewID()
	if err != nil {
		return err
	}
	nonce, err := data.NewID()
	if err != nil {
		return err
	}
	journal, err := w.runtime.state.BeginNativeInitialStage(ctx, w.stopped, nonce, workspace, copyID, sealed.hash)
	if err != nil {
		return err
	}
	w.initialStage = &nativeInitialStageWork{journal: journal, workspaceID: workspace, copyID: copyID, nonce: nonce, sourceSeal: sealed.hash, failureCode: "workspace_create"}
	w.path = filepath.Join(string(w.permit.Reserved.Database.Root), "native-restores", workspace)
	return nil
}

func newInitialPublisherParent(ctx context.Context, w *nativePrepareWork) (*os.Root, error) {
	base, err := engineOpenRoot(string(w.permit.Reserved.Database.Root))
	if err != nil {
		return nil, err
	}
	defer func() { _ = base.Close() }()
	if err = engineMakePrivateDirectoriesForRestore(base); err != nil {
		return nil, err
	}
	name := filepath.Join("native-restores", w.initialStage.workspaceID)
	if err = base.Mkdir(name, 0700); err != nil {
		return nil, err
	}
	if err = syncEngineDirectory(base, "native-restores"); err != nil {
		return nil, err
	}
	parent, err := base.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	w.parent = parent
	identity, err := nativeRestoreDirectoryIdentity(parent, ".")
	if err != nil {
		return parent, err
	}
	w.initialStage.observed.Parent = &data.EngineDirectoryIdentity{Device: identity.Device, Inode: identity.Inode}
	return parent, w.initialStage.record(ctx, "workspace_created")
}

func (w *nativeInitialStageWork) record(ctx context.Context, stage string) error {
	if w == nil {
		return nil
	}
	v := w.observed
	v.Stage, v.At, v.Code = stage, time.Now().UTC(), ""
	if stage == "unknown" {
		v.Code = w.failureCode
	}
	if err := w.journal.Record(ctx, v); err != nil {
		return err
	}
	w.observed = v
	return nil
}

func (w *nativeInitialStageWork) recordLease(ctx context.Context, lease *publicationStageLease, stage string) error {
	if w == nil {
		return nil
	}
	w.lease = lease
	if lease.containerID != "" {
		w.observed.ContainerID = lease.containerID
	}
	if lease.helperPID != 0 {
		w.observed.PID, w.observed.StartTicks, w.observed.Cgroup = lease.helperPID, lease.helperTicks, lease.helperCgroup
	}
	switch stage {
	case "started":
		stage = "helper_started"
	case "disposed":
		stage = "helper_disposed"
	case "stop_intent":
		w.failureCode = "helper_stop"
	case "unknown":
	default:
		return errEngineSource
	}
	return w.record(ctx, stage)
}

func (w *nativePrepareWork) closeInitialPublisher(ctx context.Context) error {
	stage := w.initialStage
	if stage == nil {
		return nil
	}
	if w.retainWorkspace {
		if stage.observed.Stage != "observation" {
			return errEngineSource
		}
		return closeInitialPublisherCopies(w)
	}
	if stage.observed.Stage != "observation" && stage.observed.Stage != "helper_disposed" && stage.observed.Stage != "unknown" {
		if err := stage.record(ctx, "unknown"); err != nil {
			return err
		}
	}
	if err := settleInitialPublisherHelper(ctx, stage.lease, w.runtime); err != nil {
		return err
	}
	if err := closeInitialPublisherCopies(w); err != nil {
		return err
	}
	if w.parent == nil {
		return errEngineSource
	}
	if err := observeNativeWorkspaceAbsent(ctx, w.parent); err != nil {
		return err
	}
	if stage.observed.Stage != "helper_disposed" && stage.observed.Stage != "observation" {
		if err := stage.record(ctx, "helper_disposed"); err != nil {
			return err
		}
	}
	if err := stage.record(ctx, "retirement_intent"); err != nil {
		return err
	}
	if err := discardNativeWorkspace(w.parent, w.path); err != nil {
		return err
	}
	w.parent = nil
	return stage.record(ctx, "disposed")
}

func settleInitialPublisherHelper(ctx context.Context, lease *publicationStageLease, runtime nativeBackupRuntime) error {
	if lease == nil || !lease.attempted || lease.disposed {
		return nil
	}
	if err := disposeInitialPublisherHelper(ctx, lease, runtime); err != nil {
		return err
	}
	if lease.process != nil {
		if err := descriptorProcessDisposed(ctx, lease.process); err != nil {
			return err
		}
		lease.process.close()
		lease.process = nil
	}
	lease.disposed = true
	return nil
}

func disposeInitialPublisherHelper(ctx context.Context, lease *publicationStageLease, runtime nativeBackupRuntime) error {
	containers, err := listEngineContainers(ctx, runtime.session)
	if err != nil {
		return err
	}
	matched := 0
	for _, container := range containers {
		uses := nativeUploadContainerUses(container, filepath.Dir(lease.stagePath))
		if !uses && container.Config.Labels["BRINE_PRIVATE_VERIFY"] != lease.nonce && container.Name != "brine-module-stage-"+lease.nonce {
			continue
		}
		if !ownedPublicationStage(lease, container) {
			return errEngineSource
		}
		matched++
	}
	if matched == 0 {
		return nil
	}
	if matched != 1 {
		return errEngineSource
	}
	return disposePublicationStage(ctx, lease, runtime.session)
}

func closeInitialPublisherCopies(w *nativePrepareWork) error {
	if w.copied == nil {
		return nil
	}
	copy := w.copied
	var err error
	for _, root := range []*os.Root{copy.root, copy.parent, copy.source} {
		if root != nil {
			err = errors.Join(err, root.Close())
		}
	}
	copy.root, copy.parent, copy.source = nil, nil, nil
	w.copied = nil
	return err
}

func initialPublisherCleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), jobs.JournalTimeout)
}
