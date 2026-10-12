//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"golang.org/x/sys/unix"
)

type initialPublisherEvidence struct {
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
	if lease == nil || lease.attempted || lease.sourceUnchanged(ctx) != nil {
		return initialPublisherEvidence{}, datapublish.ErrRecord
	}
	request := moduleStageInput{Action: moduleStagePublisher, Runtime: lease.permit.Runtime, Token: string(lease.material.publisher)}
	observation, err := runPublicationStage(ctx, lease, request, runner, session)
	if err != nil || observation.Action != moduleStagePublisher || !data.ValidCatalogHash(observation.Publisher) || observation.CredentialSHA256 != lease.permit.Runtime.Secrets.Publisher.SHA256 || len(observation.PublisherProof) == 0 || !observation.ChildStopped || lease.sourceUnchanged(ctx) != nil {
		return initialPublisherEvidence{}, datapublish.ErrRecord
	}
	return initialPublisherEvidence{source: lease.empty, runtime: lease.permit.Runtime.Clone(), pointID: lease.empty.stopped.PointID(), operationID: lease.empty.stopped.OperationID(), sourceSHA: lease.empty.source.SHA256(), stageRoot: lease.rootMember,
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
	wait, cancel := context.WithTimeout(ctx, time.Duration(request.Runtime.StartupSeconds+request.Runtime.StopSeconds+45)*time.Second)
	id, handle, runErr := startPublicationStageProcess(wait, lease, session)
	lease.containerID, lease.process = id, handle
	var result localexec.Output
	if runErr == nil && handle.alive() == nil && lease.sourceUnchanged(wait) == nil {
		result, runErr = runner.RunInput(wait, localexec.Command{Path: "/usr/bin/podman", Args: []string{"attach", "--sig-proxy=false", id}, Stdin: input, Mutation: true, OutputLimit: 1 << 20})
	} else {
		runErr = datapublish.ErrRecord
	}
	cancel()
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(request.Runtime.StopSeconds+10)*time.Second)
	disposeErr := disposePublicationStage(cleanup, lease, session)
	if disposeErr == nil {
		disposeErr = descriptorProcessDisposed(cleanup, handle)
	}
	stop()
	if runErr != nil || disposeErr != nil || lease.sourceUnchanged(ctx) != nil {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	handle.close()
	lease.process, lease.containerID = nil, ""
	return decodeModuleStageObservation(result.Stdout)
}

func startPublicationStageProcess(ctx context.Context, lease *publicationStageLease, session localexec.Session) (string, *engineNativeHandle, error) {
	args, err := publicationStageArguments(lease)
	if err != nil {
		return "", nil, err
	}
	started, err := session.Execute(ctx, "/usr/bin/podman", args, nil, true)
	id := strings.TrimSpace(started.Stdout)
	if err != nil || started.Truncated || !data.ValidCatalogHash(id) {
		return "", nil, datapublish.ErrRecord
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
		handle, err := bindPublicationStageProcess(lease, container)
		return id, handle, err
	}
	return id, nil, datapublish.ErrRecord
}

func bindPublicationStageProcess(lease *publicationStageLease, container engineContainer) (*engineNativeHandle, error) {
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
	if handle.alive() != nil || !engineNativeOwnership(process, pid) || !publicationStageProcessBindings(process, lease) {
		handle.close()
		return nil, datapublish.ErrRecord
	}
	return handle, nil
}

func publicationStageProcessBindings(process string, lease *publicationStageLease) bool {
	raw, err := engineProcessFile(process, "cmdline")
	if err != nil || !bytes.HasSuffix(raw, []byte{0}) || !slices.Equal(strings.Split(string(raw[:len(raw)-1]), "\x00"), []string{"/brine", "host", "module-stage"}) || !publicationStageMountedRoot(process, lease) {
		return false
	}
	for mounted, protected := range map[string]string{"exe": "/usr/local/bin/brine", "root/keys/private.pem": lease.material.privateKey, "root/keys/public.pem": lease.material.publicKey} {
		actual, err := os.Stat(filepath.Join(process, mounted))
		expected, protectedErr := os.Stat(protected)
		if err != nil || protectedErr != nil || !os.SameFile(actual, expected) {
			return false
		}
	}
	return true
}

func ownedPublicationStage(lease *publicationStageLease, container engineContainer) bool {
	runtime := lease.permit.Runtime
	if container.Name != "brine-module-stage-"+lease.nonce || container.Image != lease.image.ImageID || container.Path != "/brine" || !slices.Equal(container.Args, []string{"host", "module-stage"}) || container.Config.Labels["BRINE_PRIVATE_VERIFY"] != lease.nonce || container.Config.Labels["BRINE_MODULE_STAGE"] != lease.nonce || !engineContainerSecurity(container, runtime, "none") {
		return false
	}
	return engineContainerMounts(container.Mounts, map[string]engineMount{"/data": {Source: lease.empty.copy.path, Type: "bind", RW: engineBool(true)},
		"/keys/private.pem": {Source: lease.material.privateKey, Type: "bind", RW: engineBool(false)}, "/keys/public.pem": {Source: lease.material.publicKey, Type: "bind", RW: engineBool(false)}, "/brine": {Source: "/usr/local/bin/brine", Type: "bind", RW: engineBool(false)}})
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
		if lease.stopAttempted {
			return datapublish.ErrRecord
		}
		lease.stopAttempted = true
		if _, err = session.Execute(ctx, "/usr/bin/podman", []string{"stop", "--time", strconv.Itoa(lease.permit.Runtime.StopSeconds), container.ID}, nil, true); err != nil {
			return datapublish.ErrRecord
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
