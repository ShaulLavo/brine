//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestModuleDescriptorProfileReadsNoInputAndGrantsNoAuthority(t *testing.T) {
	var output bytes.Buffer
	input := strings.NewReader("private input must remain untouched")
	if err := RunModuleDescriptorProfile(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	var inspection descriptorProfileInspection
	if err := json.Unmarshal(output.Bytes(), &inspection); err != nil || inspection.Authority || inspection.Purpose != "module_descriptor_profile_diagnostic" || inspection.Namespace || inspection.InputKind != "unavailable" || input.Len() != len("private input must remain untouched") || len(inspection.MaskInspections) != 7 || output.Len() > 8192 {
		t.Fatal("read-only profile diagnostic acquired authority or input bytes", err)
	}
}

func TestModuleDescriptorProfileIdentifiesInputHangupWithoutReadingBytes(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if kind, closed := inspectDescriptorInput(reader); kind != "pipe" || closed {
		t.Fatal("open empty pipe looked closed")
	}
	if _, err = writer.Write([]byte("untouched")); err != nil {
		t.Fatal(err)
	}
	if kind, closed := inspectDescriptorInput(reader); kind != "pipe" || closed {
		t.Fatal("ready input changed its diagnostic state")
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if kind, closed := inspectDescriptorInput(reader); kind != "pipe" || !closed {
		t.Fatal("closed pipe did not report hangup")
	}
	buffer := make([]byte, 9)
	if read, err := reader.Read(buffer); err != nil || read != len(buffer) || string(buffer) != "untouched" {
		t.Fatal("diagnostic consumed the input payload", err)
	}
}
