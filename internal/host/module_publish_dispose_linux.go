//go:build linux

package host

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
)

func disposeDescriptorProcess(ctx context.Context, lease *descriptorLease, session localexec.Session) error {
	containers, err := listEngineContainers(ctx, session)
	if err != nil {
		return datapublish.ErrRecord
	}
	matched := 0
	for _, container := range containers {
		if container.Config.Labels["BRINE_PRIVATE_VERIFY"] != lease.artifact.nonce {
			continue
		}
		matched++
		if !ownedDescriptorContainer(lease, container) || descriptorNoSwap(ctx, session, container.ID) != nil {
			return datapublish.ErrRecord
		}
		if !*container.State.Running && *container.State.PID == 0 {
			continue
		}
		if lease.stopAttempted {
			return datapublish.ErrRecord
		}
		lease.stopAttempted = true
		if _, err = session.Execute(ctx, "/usr/bin/podman", []string{"stop", "--time", strconv.Itoa(descriptorStopSeconds), container.ID}, nil, true); err != nil {
			return datapublish.ErrRecord
		}
	}
	if matched != 1 || privateEngineDisposed(ctx, session, lease.artifact.nonce) != nil {
		return datapublish.ErrRecord
	}
	remaining, err := listEngineContainers(ctx, session)
	if err != nil {
		return datapublish.ErrRecord
	}
	for _, container := range remaining {
		if container.Config.Labels["BRINE_PRIVATE_VERIFY"] == lease.artifact.nonce {
			return datapublish.ErrRecord
		}
	}
	return nil
}

func descriptorNoSwap(ctx context.Context, session localexec.Session, id string) error {
	result, err := session.Execute(ctx, "/usr/bin/podman", []string{"container", "inspect", "--format", "{{.HostConfig.MemorySwap}}", id}, nil, false)
	if err != nil || result.Truncated || strings.TrimSpace(result.Stdout) != strconv.FormatInt(descriptorMemoryMB<<20, 10) {
		return datapublish.ErrRecord
	}
	return nil
}

func ownedDescriptorContainer(lease *descriptorLease, container engineContainer) bool {
	nonce := lease.artifact.nonce
	if container.Name != "brine-module-describe-"+nonce || container.Image != lease.image.ImageID || container.Path != "/brine" || !slices.Equal(container.Args, []string{"host", "module-describe"}) || container.Config.Labels["BRINE_MODULE_DESCRIPTOR"] != nonce || !descriptorContainerSecurity(container) {
		return false
	}
	return engineContainerMounts(container.Mounts, map[string]engineMount{
		moduleDescriptorArtifactPath: {Source: lease.artifact.path(), Type: "bind", RW: engineBool(false)},
		"/brine":                     {Source: "/usr/local/bin/brine", Type: "bind", RW: engineBool(false)},
	})
}

func descriptorContainerSecurity(container engineContainer) bool {
	c, h := container.Config, container.HostConfig
	return c.User == "1000:1000" && c.Umask == "0077" && c.StopSignal == "SIGINT" && c.StopTimeout != nil && *c.StopTimeout == descriptorStopSeconds &&
		h.NetworkMode == "none" && h.PidMode == "private" && h.CgroupManager == "systemd" && h.CgroupMode == "private" &&
		h.ReadonlyRootfs != nil && *h.ReadonlyRootfs && h.Privileged != nil && !*h.Privileged && len(h.CapAdd) == 0 && len(container.BoundingCaps) == 0 &&
		slices.Equal(h.SecurityOpt, []string{"no-new-privileges"}) && h.Memory != nil && *h.Memory == descriptorMemoryMB<<20 &&
		h.PidsLimit != nil && *h.PidsLimit == descriptorPIDs && h.NanoCpus != nil && *h.NanoCpus == 1_000_000_000
}
