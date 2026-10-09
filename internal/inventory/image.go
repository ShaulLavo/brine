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

func (c Collector) liveImage(ctx context.Context, home string, units []target.Unit, contents map[string][]byte) target.Observation[target.Image] {
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
	session, err := localexec.NewSession(executor, uint32(os.Getuid()), home, 3*time.Second)
	if err != nil {
		return unknownImage
	}
	stem := strings.TrimSuffix(container, ".container")
	name, err := podman.ParseName("systemd-" + stem)
	if err != nil {
		return unknownImage
	}
	info, err := podman.New(session).RunningContainerImage(ctx, name, stem+".service", indexPin)
	if err != nil || info.ManifestDigest != manifest || info.Platform.OS+"/"+info.Platform.Architecture != platform {
		return unknownImage
	}
	return target.Known(target.Image{Digest: info.IndexDigest, Platform: target.Platform{OS: info.Platform.OS, Arch: info.Platform.Architecture}})
}
