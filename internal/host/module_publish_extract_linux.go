//go:build linux

package host

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/store"
)

const descriptorMemoryMB = 512
const descriptorPIDs = 48
const descriptorStopSeconds = 2
const descriptorTotalWait = 20 * time.Second

type descriptorLease struct {
	artifact      *publicationArtifact
	image         podman.ImageInfo
	imageRef      string
	process       *engineNativeHandle
	containerID   string
	attempted     bool
	stopAttempted bool
	disposed      bool
	helperPID     uint32
	helperTicks   uint64
	helperCgroup  string
	startedAt     time.Time
	finishedAt    time.Time
}

func acquireDescriptorLease(ctx context.Context, state *store.Store, preparation *store.EnginePreparation, binding data.ReplicaBindingID, deploymentPlanID string, artifact datapublish.Artifact, images *podman.Client) (*descriptorLease, error) {
	if !preparation.Active(binding) {
		return nil, datapublish.ErrRecord
	}
	planned, desired, err := state.LoadPlan(ctx, deploymentPlanID)
	if err != nil || desired.SpacetimeDB == nil || planned.Managed == nil || planned.Managed.Database.BindingID != binding {
		return nil, datapublish.ErrRecord
	}
	reserved, err := state.ReadReservedEngine(ctx, binding)
	if err != nil {
		return nil, datapublish.ErrRecord
	}
	incarnation, err := state.ActiveDataIncarnation(ctx, planned.App)
	if err != nil || incarnation != reserved.Database.IncarnationID {
		return nil, datapublish.ErrRecord
	}
	_, runtime, err := state.ReadEngineHead(ctx, binding)
	if err != nil || runtime.Image != string(desired.SpacetimeDB.Image) || runtime.EpochID != planned.Managed.Archive.EpochID || planned.Managed.EngineImage.ManifestDigest.Value == nil || runtime.ManifestDigest != *planned.Managed.EngineImage.ManifestDigest.Value {
		return nil, datapublish.ErrRecord
	}
	image, err := descriptorImage(ctx, images, runtime)
	plannedImage := planned.Managed.EngineImage
	if err != nil || image.IndexDigest != plannedImage.Digest || image.Platform.OS != plannedImage.Platform.OS || image.Platform.Architecture != plannedImage.Platform.Arch || !preparation.Active(binding) {
		return nil, datapublish.ErrRecord
	}
	staged, err := stagePublicationArtifact(ctx, preparation, binding, artifact)
	if err != nil {
		return nil, err
	}
	return &descriptorLease{artifact: staged, image: image, imageRef: runtime.Image}, nil
}

func descriptorImage(ctx context.Context, images *podman.Client, runtime data.EngineRuntime) (podman.ImageInfo, error) {
	if runtime.Validate() != nil {
		return podman.ImageInfo{}, datapublish.ErrRecord
	}
	index, err := podman.ParseImage(runtime.Image)
	if err != nil {
		return podman.ImageInfo{}, datapublish.ErrRecord
	}
	repository, digest, _ := strings.Cut(runtime.Image, "@")
	selected, err := podman.ParseImage(repository + "@" + runtime.ManifestDigest)
	if err != nil {
		return podman.ImageInfo{}, datapublish.ErrRecord
	}
	observed, err := images.InspectStored(ctx, index, selected)
	if err != nil || observed.IndexDigest != digest || observed.ManifestDigest != runtime.ManifestDigest || observed.ImageID != runtime.ImageID || observed.Platform.OS != "linux" || observed.Platform.Architecture != "amd64" && observed.Platform.Architecture != "arm64" {
		return podman.ImageInfo{}, datapublish.ErrRecord
	}
	return observed, nil
}

func extractPublicationCandidate(ctx context.Context, lease *descriptorLease, runner localexec.InputRunner, session localexec.Session) (extractedPublicationCandidate, error) {
	if lease == nil || lease.artifact == nil || lease.attempted || lease.artifact.unchanged(ctx) != nil {
		return extractedPublicationCandidate{}, datapublish.ErrRecord
	}
	input, err := json.Marshal(moduleDescriptorInput{WasmSHA256: string(lease.artifact.artifact.WasmSHA256()), ProgramWireHash: string(lease.artifact.artifact.ProgramWireHash())})
	if err != nil {
		return extractedPublicationCandidate{}, datapublish.ErrRecord
	}
	wait, cancel := context.WithTimeout(ctx, descriptorTotalWait)
	lease.attempted = true
	lease.startedAt = time.Now().UTC()
	id, handle, runErr := startDescriptorProcess(wait, lease, session)
	lease.containerID, lease.process = id, handle
	var result localexec.Output
	if runErr == nil && handle.alive() == nil && lease.artifact.unchanged(wait) == nil {
		lease.helperPID, lease.helperTicks, lease.helperCgroup = handle.pid, handle.ticks, handle.cgroup
		result, runErr = runner.RunInput(wait, localexec.Command{Path: "/usr/bin/podman", Args: []string{"attach", "--sig-proxy=false", id}, Stdin: input, Mutation: true, OutputLimit: moduleDescriptorOutputLimit})
	} else {
		runErr = datapublish.ErrRecord
	}
	cancel()
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	disposeErr := disposeDescriptorProcess(cleanup, lease, session)
	if disposeErr == nil {
		disposeErr = descriptorProcessDisposed(cleanup, handle)
	}
	if disposeErr == nil && handle != nil {
		handle.close()
		lease.process = nil
		lease.containerID = ""
		lease.disposed = true
	}
	stop()
	lease.finishedAt = time.Now().UTC()
	if disposeErr != nil || runErr != nil || lease.artifact.unchanged(ctx) != nil {
		return extractedPublicationCandidate{}, datapublish.ErrRecord
	}
	schema, err := spacetimedb.DecodeDescriptorV10(result.Stdout, spacetimedb.DefaultSchemaLimits())
	if err != nil {
		return extractedPublicationCandidate{}, datapublish.ErrRecord
	}
	candidate, err := datapublish.NewCandidate(string(lease.artifact.artifact.WasmSHA256()), string(lease.artifact.artifact.ProgramWireHash()), schema)
	if err != nil {
		return extractedPublicationCandidate{}, datapublish.ErrRecord
	}
	return extractedPublicationCandidate{lease: lease, candidate: candidate, descriptor: slices.Clone(result.Stdout)}, nil
}

func descriptorArguments(lease *descriptorLease) []string {
	nonce := lease.artifact.nonce
	return []string{"run", "--detach", "--name", "brine-module-describe-" + nonce, "--label", "BRINE_PRIVATE_VERIFY=" + nonce,
		"--label", "BRINE_MODULE_DESCRIPTOR=" + nonce, "--network", "none", "--pid", "private", "--sdnotify", "ignore", "--read-only",
		"--read-only-tmpfs=false", "--passwd=false",
		"--userns", "keep-id:uid=1000,gid=1000", "--user", "1000:1000", "--umask", "0077", "--memory", "512m", "--memory-swap", "512m",
		"--pids-limit", "48", "--cpus", "1", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--stop-signal", "SIGINT",
		"--stop-timeout", "2", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=16m", "--log-driver", "none", "--interactive",
		"--volume", lease.artifact.path() + ":" + moduleDescriptorArtifactPath + ":ro", "--volume", "/usr/local/bin/brine:/brine:ro",
		"--entrypoint", "/brine", lease.imageRef, "host", "module-describe"}
}
