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
	"github.com/ShaulLavo/brine/internal/target"
)

const descriptorMemoryMB = 512
const descriptorPIDs = 48
const descriptorStopSeconds = 2
const descriptorTotalWait = 20 * time.Second

type descriptorLease struct {
	artifact    *publicationArtifact
	image       podman.ImageInfo
	imageRef    string
	process     *engineNativeHandle
	containerID string
}

func acquireDescriptorLease(ctx context.Context, state *store.Store, preparation *store.EnginePreparation, binding data.ReplicaBindingID, deploymentPlanID string, artifact datapublish.Artifact, images *podman.Client) (*descriptorLease, error) {
	if !preparation.Active(binding) {
		return nil, datapublish.ErrRecord
	}
	planned, desired, err := state.LoadPlan(ctx, deploymentPlanID)
	if err != nil || desired.SpacetimeDB == nil || string(desired.SpacetimeDB.Image) != data.SpacetimeDBImage || planned.Image.Platform.OS != "linux" {
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
	image, err := descriptorImage(ctx, images, planned.Image.Platform)
	if err != nil || !preparation.Active(binding) {
		return nil, datapublish.ErrRecord
	}
	staged, err := stagePublicationArtifact(ctx, preparation, binding, artifact)
	if err != nil {
		return nil, err
	}
	return &descriptorLease{artifact: staged, image: image, imageRef: data.SpacetimeDBImage}, nil
}

func descriptorImage(ctx context.Context, images *podman.Client, platform target.Platform) (podman.ImageInfo, error) {
	var manifest string
	switch platform.Arch {
	case "amd64":
		manifest = data.SpacetimeDBAMD64Manifest
	case "arm64":
		manifest = data.SpacetimeDBARM64Manifest
	default:
		return podman.ImageInfo{}, datapublish.ErrRecord
	}
	index, err := podman.ParseImage(data.SpacetimeDBImage)
	if err != nil {
		return podman.ImageInfo{}, datapublish.ErrRecord
	}
	selected, err := podman.ParseImage(strings.SplitN(data.SpacetimeDBImage, "@", 2)[0] + "@" + manifest)
	if err != nil {
		return podman.ImageInfo{}, datapublish.ErrRecord
	}
	observed, err := images.InspectStored(ctx, index, selected)
	if err != nil || observed.ManifestDigest != manifest || !data.ValidCatalogHash(observed.ImageID) || observed.Platform.OS != platform.OS || observed.Platform.Architecture != platform.Arch {
		return podman.ImageInfo{}, datapublish.ErrRecord
	}
	return observed, nil
}

func extractPublicationCandidate(ctx context.Context, lease *descriptorLease, runner localexec.InputRunner, session localexec.Session) (datapublish.Candidate, []byte, error) {
	if lease == nil || lease.artifact == nil || lease.artifact.unchanged(ctx) != nil {
		return datapublish.Candidate{}, nil, datapublish.ErrRecord
	}
	input, err := json.Marshal(moduleDescriptorInput{WasmSHA256: string(lease.artifact.artifact.WasmSHA256()), ProgramWireHash: string(lease.artifact.artifact.ProgramWireHash())})
	if err != nil {
		return datapublish.Candidate{}, nil, datapublish.ErrRecord
	}
	wait, cancel := context.WithTimeout(ctx, descriptorTotalWait)
	id, handle, runErr := startDescriptorProcess(wait, lease, session)
	lease.containerID, lease.process = id, handle
	var result localexec.Output
	if runErr == nil && handle.alive() == nil && lease.artifact.unchanged(wait) == nil {
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
	}
	stop()
	if disposeErr != nil || runErr != nil || lease.artifact.unchanged(ctx) != nil {
		return datapublish.Candidate{}, nil, datapublish.ErrRecord
	}
	schema, err := spacetimedb.DecodeDescriptorV10(result.Stdout, spacetimedb.DefaultSchemaLimits())
	if err != nil {
		return datapublish.Candidate{}, nil, datapublish.ErrRecord
	}
	candidate, err := datapublish.NewCandidate(string(lease.artifact.artifact.WasmSHA256()), string(lease.artifact.artifact.ProgramWireHash()), schema)
	if err != nil {
		return datapublish.Candidate{}, nil, datapublish.ErrRecord
	}
	return candidate, slices.Clone(result.Stdout), nil
}

func descriptorArguments(lease *descriptorLease) []string {
	nonce := lease.artifact.nonce
	return []string{"create", "--name", "brine-module-describe-" + nonce, "--label", "BRINE_PRIVATE_VERIFY=" + nonce,
		"--label", "BRINE_MODULE_DESCRIPTOR=" + nonce, "--network", "none", "--pid", "private", "--sdnotify", "ignore", "--read-only",
		"--read-only-tmpfs=false", "--passwd=false",
		"--userns", "keep-id:uid=1000,gid=1000", "--user", "1000:1000", "--umask", "0077", "--memory", "512m", "--memory-swap", "512m",
		"--pids-limit", "48", "--cpus", "1", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--stop-signal", "SIGINT",
		"--stop-timeout", "2", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=16m", "--log-driver", "none", "--interactive",
		"--volume", lease.artifact.path() + ":" + moduleDescriptorArtifactPath + ":ro", "--volume", "/usr/local/bin/brine:/brine:ro",
		"--entrypoint", "/brine", lease.imageRef, "host", "module-describe"}
}
