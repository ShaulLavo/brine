package enroll

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"testing"
)

func TestLitestreamPins(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		a, err := LitestreamAsset(arch)
		if err != nil {
			t.Fatal(err)
		}
		if a.Path != "/opt/brine/litestream/0.5.17/litestream" || len(a.SHA256) != 64 || a.Version != "0.5.17" {
			t.Fatal(a)
		}
	}
	if _, err := LitestreamAsset("386"); err == nil {
		t.Fatal("accepted unsupported architecture")
	}
}
func archiveFixture(t *testing.T, name string, kind byte, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Typeflag: kind, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestLitestreamExtract(t *testing.T) {
	raw := archiveFixture(t, "litestream", tar.TypeReg, []byte("fixture executable"))
	got, err := extractLitestream(raw, sha256Hex(raw))
	if err != nil || string(got) != "fixture executable" {
		t.Fatal(err)
	}
	if _, err := extractLitestream(raw, "invalid"); err == nil {
		t.Fatal("accepted incorrect checksum")
	}
	for _, name := range []string{"../litestream", "/litestream", "nested/litestream", "other"} {
		bad := archiveFixture(t, name, tar.TypeReg, []byte("fixture"))
		if _, err := extractLitestream(bad, sha256Hex(bad)); err == nil {
			t.Fatal("accepted unsafe archive", name)
		}
	}
	bad := archiveFixture(t, "litestream", tar.TypeSymlink, nil)
	if _, err := extractLitestream(bad, sha256Hex(bad)); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestVerifiedReleaseArchiveFromScratch(t *testing.T) {
	path := os.Getenv("BRINE_TEST_LITESTREAM_ARCHIVE")
	if path == "" {
		t.Skip("optional verified release archive drill")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := LitestreamAsset(os.Getenv("BRINE_TEST_LITESTREAM_ARCH"))
	if err != nil {
		t.Fatal(err)
	}
	executable, err := extractLitestream(raw, a.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if len(executable) < 1<<20 {
		t.Fatal("release executable missing")
	}
	t.Logf("version=%s arch=%s executable_sha256=%s", a.Version, a.Architecture, sha256Hex(executable))
}
