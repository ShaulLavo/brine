package enroll

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"
)

const LitestreamPath = "/opt/brine/litestream/0.5.17/litestream"
const litestreamLimit = 64 << 20

type ToolAsset struct {
	Architecture string `json:"architecture"`
	Version      string `json:"version"`
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
	Path         string `json:"path"`
}

func LitestreamAsset(arch string) (ToolAsset, error) {
	var name, checksum string
	switch arch {
	case "amd64":
		name = "x86_64"
		checksum = "cfb371176d164437ae869f8351cfde49bd1804ae71c61923f75c9cba9c9c006d"
	case "arm64":
		name = "arm64"
		checksum = "f8ca4a050095c1efbda2c4365172e61bf9d955ea0d9ac42f448b52e51819baa5"
	default:
		return ToolAsset{}, errors.New("Litestream architecture unsupported")
	}
	return ToolAsset{Architecture: arch, Version: "0.5.17", URL: "https://github.com/benbjohnson/litestream/releases/download/v0.5.17/litestream-0.5.17-linux-" + name + ".tar.gz", SHA256: checksum, Path: LitestreamPath}, nil
}
func sha256Hex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func extractLitestream(raw []byte, checksum string) ([]byte, error) {
	invalid := errors.New("Litestream archive checksum or layout refused")
	if len(raw) == 0 || len(raw) > litestreamLimit || sha256Hex(raw) != checksum {
		return nil, invalid
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, invalid
	}
	defer gz.Close()
	bounded := io.LimitReader(gz, litestreamLimit+1)
	tr := tar.NewReader(bounded)
	seen := map[string]bool{}
	var binary []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil || seen[h.Name] || h.Typeflag != tar.TypeReg || h.Size < 0 {
			return nil, invalid
		}
		seen[h.Name] = true
		switch h.Name {
		case "litestream":
			if h.Size == 0 || h.Size > litestreamLimit || h.Mode&0111 == 0 {
				return nil, invalid
			}
			binary, err = io.ReadAll(io.LimitReader(tr, litestreamLimit+1))
			if err != nil || int64(len(binary)) != h.Size {
				return nil, invalid
			}
		case "LICENSE", "README.md", "etc/litestream.service", "etc/litestream.yml":
			if h.Size > 1<<20 {
				return nil, invalid
			}
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return nil, invalid
			}
		default:
			return nil, invalid
		}
	}
	if len(binary) == 0 {
		return nil, invalid
	}

	// Drain gzip to verify its checksum, not merely the tar end marker.
	tail, err := io.ReadAll(bounded)
	if err != nil || len(tail) > 10<<10 {
		return nil, invalid
	}
	for _, b := range tail {
		if b != 0 {
			return nil, invalid
		}
	}
	return binary, nil
}
func downloadLitestream(ctx context.Context, a ToolAsset) ([]byte, error) {
	expected, err := LitestreamAsset(a.Architecture)
	if err != nil || expected != a {
		return nil, errors.New("Litestream asset is not pinned")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	client := http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" || len(via) > 5 {
			return errors.New("Litestream download redirect refused")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return nil, errors.New("Litestream download refused")
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Litestream download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("Litestream download failed")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, litestreamLimit+1))
	if err != nil {
		return nil, errors.New("Litestream download failed")
	}
	return extractLitestream(raw, a.SHA256)
}
