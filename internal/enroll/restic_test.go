package enroll

import (
	"context"
	"encoding/base64"
	"testing"
)

func TestResticAssetPinsArchiveAndExecutableSeparately(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		asset, err := ResticAsset(arch)
		if err != nil || asset.Archive.Architecture != arch || asset.Archive.Version != "0.19.1" || asset.Archive.Path != "/opt/brine/restic/0.19.1/restic" {
			t.Fatal("wrong protected restic asset", arch, err)
		}
		if len(asset.Archive.SHA256) != 64 || len(asset.BinarySHA256) != 64 || asset.Archive.SHA256 == asset.BinarySHA256 {
			t.Fatal("compressed and executable identity collapsed")
		}
		changed := asset
		changed.Archive.URL = "http://127.0.0.1/untrusted"
		if _, err := downloadRestic(context.Background(), changed); err == nil {
			t.Fatal("untrusted download location admitted")
		}
	}
	if _, err := ResticAsset("unqualified"); err == nil {
		t.Fatal("unknown architecture admitted")
	}
}

func TestResticExtractionRefusesTamperAndWrongExecutable(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString("QlpoOTFBWSZTWZAixmkAAAQRgEAAOyQWQCAAIpg0aaEDQNCTDlaJOLwOhcLuSKcKEhIEWM0g")
	if err != nil {
		t.Fatal(err)
	}
	asset := ResticToolAsset{Archive: ToolAsset{SHA256: sha256Hex(raw)}, BinarySHA256: sha256Hex([]byte("fixture executable"))}
	binary, err := extractRestic(raw, asset)
	if err != nil || string(binary) != "fixture executable" {
		t.Fatal("bounded fixture decompression failed", err)
	}
	if _, err := extractRestic(raw[:len(raw)-1], asset); err == nil {
		t.Fatal("truncated download admitted")
	}
	asset.BinarySHA256 = sha256Hex([]byte("different executable"))
	if _, err := extractRestic(raw, asset); err == nil {
		t.Fatal("wrong executable admitted")
	}
	asset.Archive.SHA256 = sha256Hex(raw[:len(raw)-1])
	if _, err := extractRestic(raw[:len(raw)-1], asset); err == nil {
		t.Fatal("authenticated truncated bzip stream admitted")
	}
}
