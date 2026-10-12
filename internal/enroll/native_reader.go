package enroll

import (
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/nativeevidence"
)

const NativeReaderPinPath = "/opt/brine/native-reader/brine-native-reader-v1/pin.json"
const nativeReaderImage = "ghcr.io/shaullavo/brine-native-reader@sha256:6ed98f489b835f7e52849f44da3edd1597c7479678d3a7492f42212c0d87f282"

type NativeReaderAsset struct {
	Format           string `json:"format"`
	SourceHead       string `json:"source_head"`
	UpstreamCommit   string `json:"upstream_commit"`
	Architecture     string `json:"architecture"`
	Image            string `json:"image"`
	ManifestDigest   string `json:"manifest_digest"`
	ImageID          string `json:"image_id"`
	ExecutableSHA256 string `json:"executable_sha256"`
}

func NativeReaderPin(arch string) (NativeReaderAsset, error) {
	pin := NativeReaderAsset{Format: "brine-native-reader-pin-v1", SourceHead: "4df496e6512efa8d6b23717edf49f9d539689c90", UpstreamCommit: nativeevidence.UpstreamCommit, Architecture: arch, Image: nativeReaderImage}
	switch arch {
	case "amd64":
		pin.ManifestDigest = "sha256:4307785471255eba25a17d8964f992df7264b643dc3538d856c1daf733c29332"
		pin.ImageID = "f1128cb88d4a105a176684244f9d24ffe87cf7274ad36207d4e81082dd730b34"
		pin.ExecutableSHA256 = "2d469cef20a93c624422c0680295cc548ad91baf28cd8c7be19a4a066ae5ad98"
	case "arm64":
		pin.ManifestDigest = "sha256:dddf898c96a42ffdc315be3a996db9754dc2f16c2a9755d54e2233cf60ae518d"
		pin.ImageID = "ad648ffbf0a36f3370727194e674d472cf66ed894943dce09547676baf75a8c2"
		pin.ExecutableSHA256 = "937742705390c14bd5bbfb232c4c1838b9e49c90bebfeb9644b567dbc143b187"
	default:
		return NativeReaderAsset{}, errors.New("native reader architecture unsupported")
	}
	return pin, nil
}
func (a NativeReaderAsset) Bytes() []byte { raw, _ := json.Marshal(a); return raw }
