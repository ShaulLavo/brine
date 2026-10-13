//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

type initialPublisherEvidence struct {
	lease          *publicationStageLease
	source         engineEmptySource
	runtime        data.EngineRuntime
	pointID        string
	operationID    string
	sourceSHA      string
	stageRoot      engineTreeMember
	publisher      string
	credentialSHA  string
	publisherProof []byte
}

func runInitialPublisherStage(ctx context.Context, lease *publicationStageLease, runner localexec.InputRunner, session localexec.Session) (initialPublisherEvidence, error) {
	if lease == nil || lease.initialStage == nil || lease.attempted || lease.sourceUnchanged(ctx) != nil {
		return initialPublisherEvidence{}, datapublish.ErrRecord
	}
	request := moduleStageInput{Action: moduleStagePublisher, Runtime: lease.permit.Runtime, Token: string(lease.material.publisher)}
	observation, err := runPublicationStage(ctx, lease, request, runner, session)
	if err != nil || observation.Action != moduleStagePublisher || !data.ValidCatalogHash(observation.Publisher) || observation.CredentialSHA256 != lease.permit.Runtime.Secrets.Publisher.SHA256 || len(observation.PublisherProof) == 0 || !observation.ChildStopped || lease.sourceUnchanged(ctx) != nil {
		return initialPublisherEvidence{}, datapublish.ErrRecord
	}
	return initialPublisherEvidence{lease: lease, source: lease.empty, runtime: lease.permit.Runtime.Clone(), pointID: lease.empty.stopped.PointID(), operationID: lease.empty.stopped.OperationID(), sourceSHA: lease.empty.source.SHA256(), stageRoot: lease.rootMember,
		publisher: observation.Publisher, credentialSHA: observation.CredentialSHA256, publisherProof: slices.Clone(observation.PublisherProof)}, nil
}

func runPublicationStage(ctx context.Context, lease *publicationStageLease, request moduleStageInput, runner localexec.InputRunner, session localexec.Session) (moduleStageObservation, error) {
	if lease == nil || lease.attempted || lease.sourceUnchanged(ctx) != nil {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	input, err := json.Marshal(request)
	if err != nil {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	defer clear(input)
	lease.attempted = true
	if lease.working != nil {
		step, err := moduleStageCursorStep(request.Action)
		if err != nil || lease.restored.state.RecordPublicationEffect(ctx, lease.working.attempt, lease.nonce, step) != nil {
			return moduleStageObservation{}, datapublish.ErrRecord
		}
		lease.effectRecorded = true
	}
	startedAt := time.Now().UTC()
	lease.startedAt = startedAt
	wait, cancel := context.WithTimeout(ctx, time.Duration(request.Runtime.StartupSeconds+request.Runtime.StopSeconds+45)*time.Second)
	if lease.initialStage != nil {
		lease.initialStage.failureCode = "container_start"
		if err = lease.initialStage.record(wait, "launch_intent"); err != nil {
			cancel()
			return moduleStageObservation{}, err
		}
	}
	id, handle, runErr := startPublicationStageProcess(wait, lease, session)
	lease.containerID, lease.process = id, handle
	var result localexec.Output
	if runErr == nil && handle.alive() == nil && lease.sourceUnchanged(wait) == nil {
		lease.helperPID, lease.helperTicks, lease.helperCgroup = handle.pid, handle.ticks, handle.cgroup
		runErr = recordPublicationStageEvent(wait, lease, "started")
		if runErr == nil {
			runErr = recordInitialPublisherInput(wait, lease)
		}
		if runErr == nil {
			result, runErr = runner.RunInput(wait, localexec.Command{Path: "/usr/bin/podman", Args: []string{"attach", "--sig-proxy=false", id}, Stdin: input, Mutation: true, OutputLimit: 1 << 20})
		}
	} else if runErr == nil {
		runErr = datapublish.ErrRecord
	}
	primaryFailure := initialPublisherFailureCode(lease, runErr, result.Stdout)
	cancel()
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(request.Runtime.StopSeconds+10)*time.Second)
	disposeErr := disposePublicationStage(cleanup, lease, session)
	if disposeErr == nil {
		disposeErr = descriptorProcessDisposed(cleanup, handle)
	}
	if disposeErr == nil && handle != nil {
		disposeErr = recordPublicationStageEvent(cleanup, lease, "disposed")
	}
	stop()
	if disposeErr == nil && handle != nil {
		handle.close()
		lease.process, lease.containerID = nil, ""
		lease.disposed = true
	}
	if runErr != nil || disposeErr != nil || lease.sourceUnchanged(ctx) != nil {
		setInitialPublisherFailure(lease, runErr, disposeErr, primaryFailure)
		_ = recordPublicationStageEvent(context.WithoutCancel(ctx), lease, "unknown")
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	lease.finishedAt = time.Now().UTC()
	observation, err := decodeModuleStageObservation(result.Stdout, startedAt, lease.finishedAt)
	if err != nil {
		if lease.initialStage != nil {
			lease.initialStage.failureCode = "helper_result"
			_ = lease.initialStage.record(context.WithoutCancel(ctx), "unknown")
		}
		return moduleStageObservation{}, err
	}
	lease.observation = observation
	if lease.initialStage != nil {
		lease.initialStage.observed.ChildPID, lease.initialStage.observed.ChildStartTicks = observation.ChildPID, observation.ChildStartTicks
		if err = lease.initialStage.record(ctx, "observation"); err != nil {
			return moduleStageObservation{}, err
		}
	}
	return observation, nil
}

func recordInitialPublisherInput(ctx context.Context, lease *publicationStageLease) error {
	if lease.initialStage == nil {
		return nil
	}
	lease.initialStage.failureCode = "input_attach"
	return lease.initialStage.record(ctx, "input_issued")
}

func initialPublisherFailureCode(lease *publicationStageLease, err error, output []byte) string {
	if lease.initialStage == nil || err == nil {
		return ""
	}
	if code := decodeModuleStageDiagnostic(output); code != "" {
		return code
	}
	return lease.initialStage.failureCode
}

func setInitialPublisherFailure(lease *publicationStageLease, primary, disposal error, code string) {
	if lease.initialStage == nil {
		return
	}
	if primary != nil {
		lease.initialStage.failureCode = code
		return
	}
	lease.initialStage.failureCode = "source_changed"
	if disposal != nil {
		lease.initialStage.failureCode = "helper_disposal"
	}
}

func classifyInitialPublisherLaunch(lease *publicationStageLease, err error) {
	if lease.initialStage == nil {
		return
	}
	lease.initialStage.failureCode = "container_start_failed"
	var command *localexec.Error
	if errors.As(err, &command) && (command.Kind == localexec.UnknownOutcome || command.Kind == localexec.Timeout) {
		lease.initialStage.failureCode = "container_start_unknown"
	}
}

func startPublicationStageProcess(ctx context.Context, lease *publicationStageLease, session localexec.Session) (string, *engineNativeHandle, error) {
	args, err := publicationStageArguments(lease)
	if err != nil {
		return "", nil, err
	}
	started, err := session.Execute(ctx, "/usr/bin/podman", args, nil, true)
	id := strings.TrimSpace(started.Stdout)
	if err != nil {
		classifyInitialPublisherLaunch(lease, err)
		return "", nil, errors.Join(datapublish.ErrRecord, err)
	}
	if started.Truncated || !data.ValidCatalogHash(id) {
		if lease.initialStage != nil {
			lease.initialStage.failureCode = "container_start_output"
		}
		return "", nil, datapublish.ErrRecord
	}
	if lease.initialStage != nil {
		lease.initialStage.failureCode = "helper_bind"
	}
	containers, err := listEngineContainers(ctx, session)
	if err != nil {
		return id, nil, datapublish.ErrRecord
	}
	for _, container := range containers {
		if container.ID != id {
			continue
		}
		if !ownedPublicationStage(lease, container) || !*container.State.Running || *container.State.PID <= 0 {
			return id, nil, datapublish.ErrRecord
		}
		handle, err := bindPublicationStageProcess(ctx, session, lease, container)
		return id, handle, err
	}
	return id, nil, datapublish.ErrRecord
}

func bindPublicationStageProcess(ctx context.Context, session localexec.Session, lease *publicationStageLease, container engineContainer) (*engineNativeHandle, error) {
	if lease == nil || container.State.Running == nil || !*container.State.Running || container.State.PID == nil || *container.State.PID <= 0 || !ownedPublicationStage(lease, container) {
		return nil, datapublish.ErrRecord
	}
	uid := strconv.Itoa(os.Geteuid())
	scope := "/user.slice/user-" + uid + ".slice/user@" + uid + ".service/user.slice/libpod-" + container.ID + ".scope"
	if container.State.CgroupPath != scope {
		return nil, datapublish.ErrRecord
	}
	process := "/proc/" + strconv.Itoa(*container.State.PID)
	raw, err := engineProcessFile(process, "stat")
	pid, ticks, parseErr := parseEngineProcessStat(string(raw))
	if err != nil || parseErr != nil || uint64(pid) != uint64(*container.State.PID) {
		return nil, datapublish.ErrRecord
	}
	fd, err := unix.PidfdOpen(int(pid), unix.PIDFD_NONBLOCK)
	if err != nil {
		return nil, datapublish.ErrRecord
	}
	handle := &engineNativeHandle{pid: pid, ticks: ticks, cgroup: scope + "/container", fd: fd}
	if handle.alive() != nil || !engineNativeOwnership(process, pid) || !publicationStageProcessBindings(ctx, session, container, handle, process, lease) {
		handle.close()
		return nil, datapublish.ErrRecord
	}
	return handle, nil
}

func publicationStageProcessBindings(ctx context.Context, session localexec.Session, container engineContainer, handle *engineNativeHandle, process string, lease *publicationStageLease) bool {
	raw, err := engineProcessFile(process, "cmdline")
	if err != nil || !bytes.HasSuffix(raw, []byte{0}) || !slices.Equal(strings.Split(string(raw[:len(raw)-1]), "\x00"), []string{"/brine", "host", "module-stage"}) {
		return false
	}
	files, err := openEngineProfileFiles(lease.stagePath, lease.material, lease.artifact)
	if err != nil {
		return false
	}
	defer files.close()
	helper, err := os.Stat(filepath.Join(process, "exe"))
	expected, expectedErr := files.helper.Stat()
	if err != nil || expectedErr != nil || !os.SameFile(helper, expected) {
		return false
	}
	root, err := files.data.Stat()
	if err != nil || !publicationStageRootIdentity(root, lease.rootMember) {
		return false
	}
	if verifyEngineMountProfile(ctx, session, container, handle, files) != nil {
		return false
	}
	if lease.artifact != nil && lease.artifact.unchanged(ctx) != nil {
		return false
	}
	return true
}

func ownedPublicationStage(lease *publicationStageLease, container engineContainer) bool {
	runtime := lease.permit.Runtime
	if container.Name != "brine-module-stage-"+lease.nonce || container.Image != lease.image.ImageID || container.Path != "/brine" || !slices.Equal(container.Args, []string{"host", "module-stage"}) || container.Config.Labels["BRINE_PRIVATE_VERIFY"] != lease.nonce || container.Config.Labels["BRINE_MODULE_STAGE"] != lease.nonce || !engineContainerSecurity(container, runtime, "none") || container.HostConfig.MemorySwap == nil || *container.HostConfig.MemorySwap != int64(runtime.MemoryMB)<<20 {
		return false
	}
	expected := map[string]engineMount{"/data": {Source: lease.stagePath, Type: "bind", RW: engineBool(true)},
		"/keys/private.pem": {Source: lease.material.privateKey, Type: "bind", RW: engineBool(false)}, "/keys/public.pem": {Source: lease.material.publicKey, Type: "bind", RW: engineBool(false)}, "/brine": {Source: "/usr/local/bin/brine", Type: "bind", RW: engineBool(false)}}
	if lease.artifact != nil {
		expected[moduleDescriptorArtifactPath] = engineMount{Source: lease.artifact.path(), Type: "bind", RW: engineBool(false)}
	}
	return engineContainerMounts(container.Mounts, expected)
}

func disposePublicationStage(ctx context.Context, lease *publicationStageLease, session localexec.Session) error {
	containers, err := listEngineContainers(ctx, session)
	if err != nil {
		return datapublish.ErrRecord
	}
	matched := 0
	for _, container := range containers {
		if container.Config.Labels["BRINE_PRIVATE_VERIFY"] != lease.nonce {
			continue
		}
		matched++
		if !ownedPublicationStage(lease, container) {
			return datapublish.ErrRecord
		}
		if !*container.State.Running && *container.State.PID == 0 {
			continue
		}
		if err = stopPublicationStageContainer(ctx, lease, session, container.ID); err != nil {
			return err
		}
	}
	if matched != 1 || privateEngineDisposed(ctx, session, lease.nonce) != nil {
		return datapublish.ErrRecord
	}
	remaining, err := listEngineContainers(ctx, session)
	if err != nil {
		return datapublish.ErrRecord
	}
	for _, container := range remaining {
		if container.Config.Labels["BRINE_PRIVATE_VERIFY"] == lease.nonce {
			return datapublish.ErrRecord
		}
	}
	return nil
}

func stopPublicationStageContainer(ctx context.Context, lease *publicationStageLease, session localexec.Session, containerID string) error {
	if lease.stopAttempted {
		return datapublish.ErrRecord
	}
	if lease.initialStage != nil {
		lease.containerID = containerID
	}
	if err := recordPublicationStageEvent(ctx, lease, "stop_intent"); err != nil {
		return err
	}
	lease.stopAttempted = true
	if _, err := session.Execute(ctx, "/usr/bin/podman", []string{"stop", "--time", strconv.Itoa(lease.permit.Runtime.StopSeconds), containerID}, nil, true); err != nil {
		return datapublish.ErrRecord
	}
	return nil
}

func recordPublicationStageEvent(ctx context.Context, lease *publicationStageLease, stage string) error {
	if lease.initialStage != nil {
		return lease.initialStage.recordLease(ctx, lease, stage)
	}
	if !lease.effectRecorded {
		return nil
	}
	if lease.working == nil || lease.restored == nil {
		return datapublish.ErrRecord
	}
	return lease.restored.state.RecordPublicationEffectEvent(ctx, lease.working.attempt, store.PublicationEffectEvent{Nonce: lease.nonce, Stage: stage, ContainerID: lease.containerID, PID: lease.helperPID, StartTicks: lease.helperTicks, Cgroup: lease.helperCgroup})
}
