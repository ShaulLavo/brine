package enroll

import (
	"bytes"
	"compress/bzip2"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/ShaulLavo/brine/internal/restic"
)

const resticArchiveLimit = 32 << 20
const resticBinaryLimit = 64 << 20

// ResticToolAsset distinguishes the authenticated download from the executable
// digest used by the protected transport boundary.
type ResticToolAsset struct {
	Archive      ToolAsset `json:"archive"`
	BinarySHA256 string    `json:"binary_sha256"`
}

func ResticAsset(arch string) (ResticToolAsset, error) {
	var archiveHash, binaryHash string
	switch arch {
	case "amd64":
		archiveHash = "f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c"
		binaryHash = "20d4142678d0d95ec11a4759def1b73fd9190abc9ca19e4b62d067c0b387e639"
	case "arm64":
		archiveHash = "a5f64aaab53d51e311fa3829124c5b703f2d14cf187d8640b6be3b2b49376465"
		binaryHash = "2fb45ac6f9071b6f20eb883953a188f9e7c7cb6bbe43c67a2e47ada4e85ee7f0"
	default:
		return ResticToolAsset{}, errors.New("restic architecture unsupported")
	}
	asset := ToolAsset{Architecture: arch, Version: restic.Version, URL: "https://github.com/restic/restic/releases/download/v" + restic.Version + "/restic_" + restic.Version + "_linux_" + arch + ".bz2", SHA256: archiveHash, Path: restic.ToolPath}
	return ResticToolAsset{Archive: asset, BinarySHA256: binaryHash}, nil
}

func extractRestic(raw []byte, asset ResticToolAsset) ([]byte, error) {
	invalid := errors.New("restic archive checksum or expansion refused")
	if len(raw) == 0 || len(raw) > resticArchiveLimit || sha256Hex(raw) != asset.Archive.SHA256 {
		return nil, invalid
	}
	reader := io.LimitReader(bzip2.NewReader(bytes.NewReader(raw)), resticBinaryLimit+1)
	binary, err := io.ReadAll(reader)
	if err != nil || len(binary) == 0 || len(binary) > resticBinaryLimit || sha256Hex(binary) != asset.BinarySHA256 {
		return nil, invalid
	}
	return binary, nil
}

func downloadRestic(ctx context.Context, asset ResticToolAsset) ([]byte, error) {
	expected, err := ResticAsset(asset.Archive.Architecture)
	if err != nil || asset != expected {
		return nil, errors.New("restic asset is not pinned")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	client := http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if request.URL.Scheme != "https" || len(via) > 5 {
			return errors.New("restic download redirect refused")
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.Archive.URL, nil)
	if err != nil {
		return nil, errors.New("restic download refused")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("restic download failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("restic download failed")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, resticArchiveLimit+1))
	if err != nil {
		return nil, errors.New("restic download failed")
	}
	return extractRestic(raw, asset)
}
