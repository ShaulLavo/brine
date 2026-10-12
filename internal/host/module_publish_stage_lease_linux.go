//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/store"
)

type publicationStageLease struct {
	empty         engineEmptySource
	preparation   *store.EnginePreparation
	permit        store.EnginePermit
	material      engineMaterial
	image         podman.ImageInfo
	nonce         string
	rootMember    engineTreeMember
	process       *engineNativeHandle
	containerID   string
	attempted     bool
	stopAttempted bool
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
	platform, err := descriptorRuntimePlatform(source.permit.Runtime)
	if err != nil {
		return nil, err
	}
	image, err := descriptorImage(ctx, images, platform)
	if err != nil || image.ImageID != source.permit.Runtime.ImageID || image.ManifestDigest != source.permit.Runtime.ManifestDigest {
		return nil, datapublish.ErrRecord
	}
	material, err := readEngineMaterial(ctx, preparation.StateRoot(), source.permit)
	if err != nil {
		return nil, datapublish.ErrRecord
	}
	nonce, err := data.NewID()
	if err != nil || source.unchanged(ctx) != nil || !preparation.Active(source.permit.Runtime.BindingID) {
		material.clear()
		return nil, datapublish.ErrRecord
	}
	return &publicationStageLease{empty: source, preparation: preparation, permit: source.permit, material: material, image: image, nonce: nonce, rootMember: sealed.root}, nil
}

func (l *publicationStageLease) sourceUnchanged(ctx context.Context) error {
	if l == nil || l.empty.unchanged(ctx) != nil || !l.preparation.Active(l.permit.Runtime.BindingID) {
		return datapublish.ErrRecord
	}
	current, err := l.empty.state.ReadNativeStoppedPermit(ctx, l.empty.stopped)
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

func publicationStageArguments(lease *publicationStageLease) ([]string, error) {
	if lease == nil || lease.permit.Runtime.Validate() != nil || !data.ValidID(lease.nonce) || !engineMountPath(lease.empty.copy.path) ||
		data.OverlappingPaths(lease.empty.copy.path, lease.permit.Reserved.Database.SourcePath()) || !engineMountPath(lease.material.privateKey) || !engineMountPath(lease.material.publicKey) {
		return nil, datapublish.ErrRecord
	}
	runtime := lease.permit.Runtime
	args := engineContainerBase(runtime, "brine-module-stage-"+lease.nonce, "none")
	return append(args, "--detach", "--interactive", "--pid", "private", "--cgroupns", "private", "--read-only-tmpfs=false", "--passwd=false",
		"--memory-swap", strconv.Itoa(runtime.MemoryMB)+"m", "--label", "BRINE_PRIVATE_VERIFY="+lease.nonce, "--label", "BRINE_MODULE_STAGE="+lease.nonce,
		"--volume", lease.empty.copy.path+":/data:rw", "--volume", lease.material.privateKey+":/keys/private.pem:ro", "--volume", lease.material.publicKey+":/keys/public.pem:ro",
		"--volume", "/usr/local/bin/brine:/brine:ro", "--entrypoint", "/brine", runtime.Image, "host", "module-stage"), nil
}

func publicationStageMountedRoot(process string, lease *publicationStageLease) bool {
	actual, err := os.Stat(filepath.Join(process, "root/data"))
	if err != nil || !actual.IsDir() {
		return false
	}
	member, ok := actual.Sys().(*syscall.Stat_t)
	expected := lease.rootMember
	return ok && uint64(member.Dev) == expected.Device && member.Ino == expected.Inode && member.Uid == expected.UID && uint16(member.Mode) == expected.Mode
}
