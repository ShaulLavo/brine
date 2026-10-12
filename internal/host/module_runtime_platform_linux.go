//go:build linux

package host

import (
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/target"
)

func descriptorRuntimePlatform(runtime data.EngineRuntime) (target.Platform, error) {
	if runtime.Image != data.SpacetimeDBImage || runtime.Version != "2.11.0" {
		return target.Platform{}, datapublish.ErrRecord
	}
	switch runtime.ManifestDigest {
	case data.SpacetimeDBAMD64Manifest:
		return target.Platform{OS: "linux", Arch: "amd64"}, nil
	case data.SpacetimeDBARM64Manifest:
		return target.Platform{OS: "linux", Arch: "arm64"}, nil
	default:
		return target.Platform{}, datapublish.ErrRecord
	}
}
