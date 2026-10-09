package inventory

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/target"
)

func (c Collector) liveImage(ctx context.Context, home string, units []target.Unit, contents map[string][]byte, inactive bool) target.Observation[target.Image] {
	unknownImage := unknown[target.Image]()
	container := ""
	for _, unit := range units {
		if strings.HasSuffix(unit.Name, ".container") {
			if container != "" {
				return unknownImage
			}
			container = unit.Name
		}
	}
	data := contents[container]
	if !renderedUnitMarker.Match(data) {
		return unknownImage
	}
	// The renderer pins the selected manifest in Image= and records the index
	// separately. Neither comment becomes a fact until Podman verifies it.
	section, reference, index, manifest, platform := "", "", "", "", ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "# IndexDigest="):
			if index != "" {
				return unknownImage
			}
			index = strings.TrimPrefix(line, "# IndexDigest=")
		case strings.HasPrefix(line, "# PlatformManifestDigest="):
			if manifest != "" {
				return unknownImage
			}
			manifest = strings.TrimPrefix(line, "# PlatformManifestDigest=")
		case strings.HasPrefix(line, "# Platform="):
			if platform != "" {
				return unknownImage
			}
			platform = strings.TrimPrefix(line, "# Platform=")
		case strings.HasPrefix(line, "["):
			section = line
		case strings.HasPrefix(line, "Image=") && section == "[Container]":
			if reference != "" {
				return unknownImage
			}
			reference = strings.TrimPrefix(line, "Image=")
		case strings.HasPrefix(line, "ContainerName=") && section == "[Container]":
			return unknownImage
		}
	}
	pin, err := podman.ParseImage(reference)
	if err != nil || manifest == "" || !strings.HasSuffix(pin.String(), "@"+manifest) {
		return unknownImage
	}
	repository, _, _ := strings.Cut(pin.String(), "@")
	indexPin, err := podman.ParseImage(repository + "@" + index)
	if err != nil {
		return unknownImage
	}
	executor, ok := c.Runner.(localexec.Executor)
	if !ok {
		return unknownImage
	}
	// Index inspection can require a registry read, unlike local host probes.
	// Collection's one-minute deadline still bounds all apps together.
	session, err := localexec.NewSession(executor, uint32(os.Getuid()), home, 30*time.Second)
	if err != nil {
		return unknownImage
	}
	stem := strings.TrimSuffix(container, ".container")
	name, err := podman.ParseName("systemd-" + stem)
	if err != nil {
		return unknownImage
	}
	client := podman.New(session)
	var info podman.ImageInfo
	if inactive {
		info, err = client.StoppedContainerImage(ctx, name, stem+".service", indexPin, pin)
	} else {
		info, err = client.RunningContainerImage(ctx, name, stem+".service", indexPin)
	}
	if err != nil || info.ManifestDigest != manifest || info.Platform.OS+"/"+info.Platform.Architecture != platform {
		return unknownImage
	}
	return target.Known(target.Image{Digest: info.IndexDigest, Platform: target.Platform{OS: info.Platform.OS, Arch: info.Platform.Architecture}})
}

func (c Collector) images(ctx context.Context, s *target.Snapshot, home string, artifacts appArtifacts) {
	if !artifacts.runner || s.Apps.Status != target.KnownStatus {
		return
	}
	apps := *s.Apps.Value
	pending := []int{}
	for i, app := range apps {
		if app.Image.Status == target.Unknown && app.QuadletUnits.Status == target.KnownStatus && len(*app.QuadletUnits.Value) > 0 {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 {
		return
	}
	// Images are optional facts. Keep at least half the caller's remaining time
	// outside their aggregate budget, and never spend more than thirty seconds.
	budget := 30 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline) / 2; remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		return
	}
	probes, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	deadline, _ := probes.Deadline()
	for position, index := range pending {
		remaining := time.Until(deadline)
		if probes.Err() != nil || remaining <= 0 {
			break
		}
		slice := remaining / time.Duration(len(pending)-position)
		if slice <= 0 {
			break
		}
		// Transitional/failed unit states must not become stopped evidence; a
		// running container contradicting a known inactive unit stays unknown.
		app := apps[index]
		if app.UnitActive != nil && app.UnitActive.Status == target.KnownStatus && !*app.UnitActive.Value && !artifacts.inactive[app.Name] {
			continue
		}

		appCtx, stop := context.WithTimeout(probes, slice)
		apps[index].Image = c.liveImage(appCtx, home, *apps[index].QuadletUnits.Value, artifacts.containers, artifacts.inactive[apps[index].Name])
		// A stopped owned unit retains its allocation, not a listener. Do not add
		// it to publications: any socket on this port must still prove live ownership.
		if artifacts.inactive[apps[index].Name] && apps[index].Image.Status == target.KnownStatus {
			for _, unit := range *apps[index].QuadletUnits.Value {
				if strings.HasSuffix(unit.Name, ".container") {
					if port, ok := pinnedListenerPort(string(artifacts.containers[unit.Name])); ok {
						apps[index].AllocatedHostPort = target.Known(port.Host)
					}
				}
			}
		}
		stop()
	}
}
