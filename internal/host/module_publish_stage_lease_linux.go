//go:build linux

package host

import (
	"context"
	"os"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/store"
)

type publicationStageLease struct {
	empty          engineEmptySource
	restored       *nativeRestoredSource
	working        *publicationWorkingStage
	review         *publicationReviewWork
	artifact       *publicationArtifact
	stagePath      string
	preparation    *store.EnginePreparation
	permit         store.EnginePermit
	material       engineMaterial
	image          podman.ImageInfo
	nonce          string
	rootMember     engineTreeMember
	process        *engineNativeHandle
	containerID    string
	attempted      bool
	stopAttempted  bool
	disposed       bool
	startedAt      time.Time
	finishedAt     time.Time
	helperPID      uint32
	helperTicks    uint64
	helperCgroup   string
	observation    moduleStageObservation
	effectRecorded bool
	failure        *datapublish.StageFailure
	initialStage   *nativeInitialStageWork
}

func acquireInitialPublisherStage(ctx context.Context, source engineEmptySource, images *podman.Client) (*publicationStageLease, error) {
	if source.unchanged(ctx) != nil || source.permit.Runtime.State != data.EngineUnpublished {
		return nil, datapublish.ErrRecord
	}
	preparation, err := source.state.ReadNativeStoppedDescriptorPreparation(ctx, source.stopped)
	if err != nil {
		return nil, datapublish.ErrRecord
	}
	sealed, err := sealEngineRoot(ctx, source.copy.root, source.copy.limits)
	if err != nil || !sameEngineContents(source.original, sealed) {
		return nil, datapublish.ErrRecord
	}
	if source.initialStage != nil {
		source.initialStage.failureCode = "image_inspect"
		if err = source.initialStage.record(ctx, "image_inspect"); err != nil {
			return nil, err
		}
	}
	image, err := descriptorImage(ctx, images, source.permit.Runtime)
	if err != nil {
		return nil, datapublish.ErrRecord
	}
	if source.initialStage != nil {
		if err = source.initialStage.record(ctx, "image_ready"); err != nil {
			return nil, err
		}
		source.initialStage.failureCode = "material_read"
	}
	material, err := readEngineMaterial(ctx, preparation.StateRoot(), source.permit)
	if err != nil {
		return nil, datapublish.ErrRecord
	}
	if source.initialStage != nil {
		if err = source.initialStage.record(ctx, "material_ready"); err != nil {
			material.clear()
			return nil, err
		}
	}
	nonce, err := data.NewID()
	if err != nil || source.unchanged(ctx) != nil || !preparation.Active(source.permit.Runtime.BindingID) {
		material.clear()
		return nil, datapublish.ErrRecord
	}
	if source.initialStage != nil {
		nonce = source.initialStage.nonce
		source.initialStage.failureCode = "lease_check"
		if err = source.initialStage.record(ctx, "lease_ready"); err != nil {
			material.clear()
			return nil, err
		}
	}
	lease := &publicationStageLease{empty: source, stagePath: source.copy.path, preparation: preparation, permit: source.permit, material: material, image: image, nonce: nonce, rootMember: sealed.root, initialStage: source.initialStage}
	if source.initialStage != nil {
		source.initialStage.lease = lease
	}
	return lease, nil
}

func acquireRestoredPublicationStage(ctx context.Context, working *publicationWorkingStage, images *podman.Client) (*publicationStageLease, error) {
	if working.unchanged(ctx) != nil {
		return nil, datapublish.ErrRecord
	}
	lease, err := acquireRestoredStage(ctx, working.source, working.artifact, images)
	if err != nil {
		return nil, err
	}
	lease.working = working
	if lease.sourceUnchanged(ctx) != nil {
		lease.material.clear()
		return nil, datapublish.ErrRecord
	}
	return lease, nil
}

func acquireRestoredStage(ctx context.Context, source *nativeRestoredSource, artifact *publicationArtifact, images *podman.Client) (*publicationStageLease, error) {
	if source == nil || source.originalUnchanged(ctx) != nil || artifact == nil || artifact.preparation != source.preparation || artifact.unchanged(ctx) != nil || !publicationStageRootUnchanged(source.root, source.path, source.seal.root) {
		return nil, datapublish.ErrRecord
	}
	point, err := source.state.ReadNativePreparedPoint(ctx, source.pointID)
	if err != nil || point.PointSHA256 != source.pointSHA256 || point.BindingID != source.permit.Runtime.BindingID || point.EpochID != source.permit.Runtime.EpochID || point.SourceSHA256 != source.source.SHA256() || point.CheckpointSHA256 != source.checkpointSHA256 || source.manifest.Identity().CheckpointSHA256 != source.checkpointSHA256 || point.BundleManifestSHA256 != source.manifest.SHA256() {
		return nil, datapublish.ErrRecord
	}
	image, err := descriptorImage(ctx, images, source.permit.Runtime)
	if err != nil {
		return nil, datapublish.ErrRecord
	}
	material, err := readEngineMaterial(ctx, source.preparation.StateRoot(), source.permit)
	if err != nil {
		return nil, datapublish.ErrRecord
	}
	nonce, err := data.NewID()
	if err != nil || source.originalUnchanged(ctx) != nil || artifact.unchanged(ctx) != nil {
		material.clear()
		return nil, datapublish.ErrRecord
	}
	return &publicationStageLease{restored: source, artifact: artifact, stagePath: source.path, preparation: source.preparation, permit: source.permit, material: material, image: image, nonce: nonce, rootMember: source.seal.root}, nil
}

func (l *publicationStageLease) sourceUnchanged(ctx context.Context) error {
	if l == nil || !l.preparation.Active(l.permit.Runtime.BindingID) {
		return datapublish.ErrRecord
	}
	current, err := l.currentPermit(ctx)
	if err != nil || !sameEnginePermit(l.permit, current) {
		return datapublish.ErrRecord
	}
	material, err := readEngineMaterial(ctx, l.preparation.StateRoot(), current)
	if err != nil {
		return datapublish.ErrRecord
	}
	defer material.clear()
	if !slices.Equal(material.publisher, l.material.publisher) || material.privateKey != l.material.privateKey || material.publicKey != l.material.publicKey {
		return datapublish.ErrRecord
	}
	return nil
}

func (l *publicationStageLease) currentPermit(ctx context.Context) (store.EnginePermit, error) {
	if l.restored != nil && l.empty.state == nil {
		if !l.restoredAuthorityUnchanged(ctx) {
			return store.EnginePermit{}, datapublish.ErrRecord
		}
		if !publicationStageRootUnchanged(l.restored.root, l.stagePath, l.rootMember) {
			return store.EnginePermit{}, datapublish.ErrRecord
		}
		current, err := l.restored.state.ReadEnginePermit(ctx, l.permit.Runtime.BindingID)
		if err != nil || !sameEnginePermit(current, l.permit) {
			return store.EnginePermit{}, datapublish.ErrRecord
		}
		return current, nil
	}
	if l.restored != nil || l.empty.unchanged(ctx) != nil {
		return store.EnginePermit{}, datapublish.ErrRecord
	}
	return l.empty.state.ReadNativeStoppedPermit(ctx, l.empty.stopped)
}

func (l *publicationStageLease) restoredAuthorityUnchanged(ctx context.Context) bool {
	if l.working != nil && l.review == nil {
		return l.working.source == l.restored && l.working.unchanged(ctx) == nil
	}
	return l.review != nil && l.working == nil && l.review.source == l.restored && l.review.unchanged(ctx) == nil
}

func publicationStageRootUnchanged(root *os.Root, path string, expected engineTreeMember) bool {
	held, err := nativeRestoreDirectoryIdentity(root, ".")
	if err != nil || !sameNativeRestoreDirectory(held, expected) {
		return false
	}
	current, err := engineOpenRoot(path)
	if err != nil {
		return false
	}
	defer func() { _ = current.Close() }()
	observed, err := nativeRestoreDirectoryIdentity(current, ".")
	return err == nil && sameNativeRestoreDirectory(observed, expected)
}

func publicationStageArguments(lease *publicationStageLease) ([]string, error) {
	if lease == nil || lease.permit.Runtime.Validate() != nil || !data.ValidID(lease.nonce) || !engineMountPath(lease.stagePath) ||
		data.OverlappingPaths(lease.stagePath, lease.permit.Reserved.Database.SourcePath()) || !engineMountPath(lease.material.privateKey) || !engineMountPath(lease.material.publicKey) {
		return nil, datapublish.ErrRecord
	}
	runtime := lease.permit.Runtime
	args := engineContainerBase(runtime, "brine-module-stage-"+lease.nonce, "none")
	args = append(args, "--detach", "--interactive", "--pid", "private", "--cgroupns", "private", "--read-only-tmpfs=false", "--passwd=false",
		"--memory-swap", strconv.Itoa(runtime.MemoryMB)+"m", "--label", "BRINE_PRIVATE_VERIFY="+lease.nonce, "--label", "BRINE_MODULE_STAGE="+lease.nonce,
		"--volume", lease.stagePath+":/data:rw", "--volume", lease.material.privateKey+":/keys/private.pem:ro", "--volume", lease.material.publicKey+":/keys/public.pem:ro",
		"--volume", "/usr/local/bin/brine:/brine:ro")
	if lease.artifact != nil {
		args = append(args, "--volume", lease.artifact.path()+":"+moduleDescriptorArtifactPath+":ro")
	}
	return append(args, "--entrypoint", "/brine", runtime.Image, "host", "module-stage"), nil
}

func publicationStageRootIdentity(actual os.FileInfo, expected engineTreeMember) bool {
	member, ok := actual.Sys().(*syscall.Stat_t)
	return ok && actual.IsDir() && uint64(member.Dev) == expected.Device && member.Ino == expected.Inode && member.Uid == expected.UID && member.Mode == uint32(expected.Mode)
}
