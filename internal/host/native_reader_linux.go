//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

const nativeReaderPinPath = "/opt/brine/native-reader/brine-native-reader-v1/pin.json"
const nativeReaderRepository = "ghcr.io/shaullavo/brine-native-reader"

var nativeReaderSourceHead = regexp.MustCompile(`^[0-9a-f]{40}$`)

type nativeReaderPin struct {
	Format           string `json:"format"`
	SourceHead       string `json:"source_head"`
	UpstreamCommit   string `json:"upstream_commit"`
	Architecture     string `json:"architecture"`
	Image            string `json:"image"`
	ManifestDigest   string `json:"manifest_digest"`
	ImageID          string `json:"image_id"`
	ExecutableSHA256 string `json:"executable_sha256"`
}

func readNativeReaderPin(ctx context.Context) (nativeReaderPin, error) {
	raw, err := trustedRead(ctx, nativeReaderPinPath, 4096)
	if err != nil {
		return nativeReaderPin{}, errEngineSource
	}
	return decodeNativeReaderPin(raw, runtime.GOARCH)
}

func decodeNativeReaderPin(raw []byte, arch string) (nativeReaderPin, error) {
	if len(raw) > 4096 {
		return nativeReaderPin{}, errEngineSource
	}
	if _, err := strictjson.Object(raw, "format", "source_head", "upstream_commit", "architecture", "image", "manifest_digest", "image_id", "executable_sha256"); err != nil {
		return nativeReaderPin{}, errEngineSource
	}
	var pin nativeReaderPin
	if json.Unmarshal(raw, &pin) != nil || pin.Format != "brine-native-reader-pin-v1" || pin.UpstreamCommit != nativeevidence.UpstreamCommit || !nativeReaderSourceHead.MatchString(pin.SourceHead) || pin.Architecture != arch || arch != "amd64" && arch != "arm64" || !data.ValidCatalogHash(pin.ImageID) || !data.ValidCatalogHash(pin.ExecutableSHA256) {
		return nativeReaderPin{}, errEngineSource
	}
	index, err := podman.ParseImage(pin.Image)
	if err != nil || index.String() != pin.Image || !strings.HasPrefix(pin.Image, nativeReaderRepository+"@sha256:") || !strings.HasPrefix(pin.ManifestDigest, "sha256:") || !data.ValidCatalogHash(strings.TrimPrefix(pin.ManifestDigest, "sha256:")) {
		return nativeReaderPin{}, errEngineSource
	}
	return pin, nil
}

func nativeReaderLimits() nativeevidence.Limits {
	l := nativeevidence.MaximumLimits()
	l.StoredBytes, l.FileBytes = 64<<20, 32<<20
	l.ExpandedBytes, l.ExpandedFileBytes = 128<<20, 64<<20
	l.StateBytes = 64 << 20
	return l
}

func nativeReaderInput(checkpoint nativecapture.Checkpoint, selection nativecapture.Selection) ([]byte, error) {
	record := checkpoint.Record()
	scope := selection.Scope()
	if checkpoint.SHA256() == "" || record.Scope.BindingID != scope.BindingID || record.Scope.EpochID != scope.EpochID || record.Scope.ReplicaID != scope.ReplicaID {
		return nil, errEngineSource
	}
	return marshalNativeReaderInput(nativeKnownIdentity{"known", record.DatabaseIdentity, record.PublisherIdentity}, selection)
}

type nativeReaderIdentity interface{ nativeIdentity() }
type nativeKnownIdentity struct {
	Kind     string `json:"kind"`
	Database string `json:"database_identity"`
	Owner    string `json:"owner_identity"`
}

func (nativeKnownIdentity) nativeIdentity() {}

type nativeDiscoveryIdentity struct {
	Kind  string `json:"kind"`
	Owner string `json:"owner_identity"`
}

func (nativeDiscoveryIdentity) nativeIdentity() {}

func nativeDiscoveryInput(protectedOwner string, selection nativecapture.Selection) ([]byte, error) {
	if !data.ValidCatalogHash(protectedOwner) {
		return nil, errEngineSource
	}
	return marshalNativeReaderInput(nativeDiscoveryIdentity{"discover", protectedOwner}, selection)
}

func marshalNativeReaderInput(identity nativeReaderIdentity, selection nativecapture.Selection) ([]byte, error) {
	var history any
	switch selection.Kind() {
	case "logs":
		history = struct {
			Kind string `json:"kind"`
		}{"logs"}
	case "snapshot":
		offset, present := selection.SnapshotOffset()
		if !present {
			return nil, errEngineSource
		}
		history = struct {
			Kind    string `json:"kind"`
			Offset  uint64 `json:"offset"`
			Replica uint64 `json:"replica_id"`
		}{"snapshot", offset, selection.Scope().ReplicaID}
	default:
		return nil, errEngineSource
	}
	raw, err := json.Marshal(struct {
		Format   string                `json:"format"`
		Identity nativeReaderIdentity  `json:"identity"`
		History  any                   `json:"history"`
		Limits   nativeevidence.Limits `json:"limits"`
	}{nativeevidence.Format, identity, history, nativeReaderLimits()})
	if err != nil || len(raw) > 8192 {
		return nil, errEngineSource
	}
	return raw, nil
}

func nativeReaderArguments(pin nativeReaderPin, view *nativeReadView) ([]string, error) {
	if view == nil || view.state != nativeViewPrepared || !data.ValidID(view.nonce) || !engineMountPath(view.path) {
		return nil, errEngineSource
	}
	name := "brine-native-read-" + view.nonce
	return []string{"run", "--interactive", "--name", name, "--label", "BRINE_PRIVATE_VERIFY=" + view.nonce, "--hostname", "brine-native-reader", "--network", "none", "--read-only", "--read-only-tmpfs=false", "--passwd=false", "--userns", "keep-id:uid=1000,gid=1000", "--user", "1000:1000", "--umask", "0077", "--pid", "private", "--cgroupns", "private", "--memory", "512m", "--memory-swap", "512m", "--pids-limit", "16", "--cpus", "1", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--stop-signal", "SIGINT", "--stop-timeout", "5", "--unsetenv-all", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec", "--log-driver", "none", "--volume", filepath.Join(view.path, view.replica) + ":/copy/replica:rw", "--entrypoint", "/brine-native-reader", pin.Image}, nil
}

type nativeDisposal struct {
	nonce    string
	viewHash string
	imageID  string
	at       time.Time
}

func readNativeView(ctx context.Context, session localexec.Session, pin nativeReaderPin, view *nativeReadView, checkpoint nativecapture.Checkpoint) (nativeevidence.Evidence, nativeDisposal, error) {
	if view == nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, errEngineSource
	}
	input, err := nativeReaderInput(checkpoint, view.selection)
	if err != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, err
	}
	observed, disposed, err := runNativeReadView(ctx, session, pin, view, input)
	if err != nil || !nativeEvidenceMatches(checkpoint, view.selection, observed) {
		return nativeevidence.Evidence{}, nativeDisposal{}, errEngineSource
	}
	return observed, disposed, nil
}

func readNativeDiscoveryView(ctx context.Context, session localexec.Session, pin nativeReaderPin, view *nativeReadView, protectedOwner string) (nativeevidence.Evidence, nativeDisposal, error) {
	if view == nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, errEngineSource
	}
	input, err := nativeDiscoveryInput(protectedOwner, view.selection)
	if err != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, err
	}
	observed, disposed, err := runNativeReadView(ctx, session, pin, view, input)
	if err != nil || !nativeDiscoveryMatches(protectedOwner, view.selection, observed) {
		return nativeevidence.Evidence{}, nativeDisposal{}, errEngineSource
	}
	return observed, disposed, nil
}

func nativeDiscoveryMatches(protectedOwner string, selection nativecapture.Selection, observed nativeevidence.Evidence) bool {
	if !data.ValidCatalogHash(protectedOwner) || observed.OwnerIdentity() != protectedOwner {
		return false
	}
	snapshot, present := observed.Snapshot()
	offset, selected := selection.SnapshotOffset()
	return present == selected && (!selected || snapshot.Database == observed.DatabaseIdentity() && snapshot.Replica == selection.Scope().ReplicaID && snapshot.Offset == offset)
}

func runNativeReadView(ctx context.Context, session localexec.Session, pin nativeReaderPin, view *nativeReadView, input []byte) (nativeevidence.Evidence, nativeDisposal, error) {
	if view == nil || view.unchanged(ctx) != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, errEngineSource
	}
	protected, err := readNativeReaderPin(ctx)
	if err != nil || protected != pin {
		return nativeevidence.Evidence{}, nativeDisposal{}, errEngineSource
	}
	index, err := podman.ParseImage(pin.Image)
	if err != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, errEngineSource
	}
	manifest, err := podman.ParseImage(nativeReaderRepository + "@" + pin.ManifestDigest)
	if err != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, errEngineSource
	}
	image, err := podman.New(session).InspectStored(ctx, index, manifest)
	if err != nil || image.ImageID != pin.ImageID || image.Platform.OS != "linux" || image.Platform.Architecture != pin.Architecture {
		return nativeevidence.Evidence{}, nativeDisposal{}, errEngineSource
	}
	args, err := nativeReaderArguments(pin, view)
	if err != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, err
	}
	var output bytes.Buffer
	view.state = nativeViewReading
	runErr := session.RunStreams(ctx, "/usr/bin/podman", args, bytes.NewReader(input), &output, 1<<20, true)
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	disposed, disposeErr := disposeNativeReader(cleanup, session, pin, view, runErr == nil)
	if disposeErr != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, errEngineSource
	}
	view.state = nativeViewDisposed
	if runErr != nil || view.unchanged(ctx) != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, errEngineSource
	}
	observed, err := nativeevidence.Decode(output.Bytes(), nativeReaderLimits())
	if err != nil {
		return nativeevidence.Evidence{}, nativeDisposal{}, errEngineSource
	}
	return observed, disposed, nil
}

func nativeEvidenceMatches(checkpoint nativecapture.Checkpoint, selection nativecapture.Selection, observed nativeevidence.Evidence) bool {
	r := checkpoint.Record()
	program := observed.Program()
	if observed.DatabaseIdentity() != r.DatabaseIdentity || observed.OwnerIdentity() != r.PublisherIdentity || program.Wire != r.RuntimeModuleHash || program.SHA256 != r.ArtifactSHA256 {
		return false
	}
	snapshot, present := observed.Snapshot()
	offset, selected := selection.SnapshotOffset()
	return present == selected && (!selected || snapshot.Database == r.DatabaseIdentity && snapshot.Replica == selection.Scope().ReplicaID && snapshot.Offset == offset)
}

func disposeNativeReader(ctx context.Context, session localexec.Session, pin nativeReaderPin, view *nativeReadView, requireObserved bool) (nativeDisposal, error) {
	containers, err := listEngineContainers(ctx, session)
	if err != nil {
		return nativeDisposal{}, errEngineSource
	}
	found := false
	for _, container := range containers {
		if container.Config.Labels["BRINE_PRIVATE_VERIFY"] != view.nonce {
			continue
		}
		if found || !stoppedNativeReader(container, pin, view) {
			return nativeDisposal{}, errEngineSource
		}
		found = true
	}
	if requireObserved && !found {
		return nativeDisposal{}, errEngineSource
	}
	if privateEngineDisposed(ctx, session, view.nonce) != nil {
		return nativeDisposal{}, errEngineSource
	}
	containers, err = listEngineContainers(ctx, session)
	if err != nil {
		return nativeDisposal{}, errEngineSource
	}
	for _, container := range containers {
		if container.Config.Labels["BRINE_PRIVATE_VERIFY"] == view.nonce {
			return nativeDisposal{}, errEngineSource
		}
	}
	return nativeDisposal{nonce: view.nonce, viewHash: view.seal.hash, imageID: pin.ImageID, at: time.Now().UTC()}, nil
}

func stoppedNativeReader(container engineContainer, pin nativeReaderPin, view *nativeReadView) bool {
	if container.Name != "brine-native-read-"+view.nonce || container.Image != pin.ImageID || container.Path != "/brine-native-reader" || !slices.Equal(container.Args, []string{"/brine-native-reader"}) || container.State.Running == nil || container.State.PID == nil || *container.State.Running || *container.State.PID != 0 {
		return false
	}
	if !engineContainerSecurity(container, data.EngineRuntime{MemoryMB: 512, PIDsLimit: 16, StopSeconds: 5}, "none") {
		return false
	}
	return engineContainerMounts(container.Mounts, map[string]engineMount{"/copy/replica": {Type: "bind", Source: filepath.Join(view.path, view.replica), RW: engineBool(true)}})
}
