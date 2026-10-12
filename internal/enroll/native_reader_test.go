package enroll

import "testing"

func TestNativeReaderEnrollmentUsesPublishedMainBytesForEachPlatform(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		pin, err := NativeReaderPin(arch)
		if err != nil || pin.SourceHead != "89c8a13934b964d3e89d4d1dc528be9d6fdba05e" || pin.Image != nativeReaderImage || pin.Architecture != arch || len(pin.ImageID) != 64 || len(pin.ExecutableSHA256) != 64 || len(pin.ManifestDigest) != 71 || len(pin.Bytes()) > 4096 {
			t.Fatal("published native reader pin mismatch", arch, err)
		}
	}
	if _, err := NativeReaderPin("unknown"); err == nil {
		t.Fatal("unpublished native reader platform admitted")
	}
}
