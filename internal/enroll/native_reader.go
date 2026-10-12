package enroll

import (
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/nativeevidence"
)

const NativeReaderPinPath = "/opt/brine/native-reader/brine-native-reader-v1/pin.json"
const nativeReaderImage = "ghcr.io/shaullavo/brine-native-reader@sha256:5d3e27f26928def7d56909a4eecab4bf2ea0866bb0003099541b2c5782c10c9b"

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
	pin := NativeReaderAsset{Format: "brine-native-reader-pin-v1", SourceHead: "89c8a13934b964d3e89d4d1dc528be9d6fdba05e", UpstreamCommit: nativeevidence.UpstreamCommit, Architecture: arch, Image: nativeReaderImage}
	switch arch {
	case "amd64":
		pin.ManifestDigest = "sha256:49040092f67c286aa2ba4c9eb3c3cd99e69897813adf402ffc0627697ab33c6d"
		pin.ImageID = "83d776bfa84e994818ecfc4fdb7e19b7d269ff2078721034c8f9e228251d9a5e"
		pin.ExecutableSHA256 = "9c98a9222e53d7f3fc0fdfa4e3081fbc0cdae81a5c03de16cbd7584db7321000"
	case "arm64":
		pin.ManifestDigest = "sha256:45f4b6b13393ff425ac16e07e6550a45d24821903a25eb3f61db0c9bb7fedd64"
		pin.ImageID = "711844d4fa557a7a1667a8d8ef97110dc55ac823f2baf822ba0236f98ab79203"
		pin.ExecutableSHA256 = "a22d5a5b541c20594a5804e1bc1d3889a0d437c01c77953d06dfc56eb342d684"
	default:
		return NativeReaderAsset{}, errors.New("native reader architecture unsupported")
	}
	return pin, nil
}
func (a NativeReaderAsset) Bytes() []byte { raw, _ := json.Marshal(a); return raw }
