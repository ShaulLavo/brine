package cli

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestContainedMountProfileAcceptsNoCallerPathOrInput(t *testing.T) {
	calls := 0
	deps := Dependencies{Stdout: &bytes.Buffer{}, HostEngineMountProfile: func(context.Context, io.Writer) error { calls++; return nil }}
	command := newHostEngineMountProfile(deps)
	command.SetArgs([]string{"/caller/path"})
	if command.Execute() == nil || calls != 0 {
		t.Fatal("caller pathname reached fixed helper")
	}
	command = newHostEngineMountProfile(deps)
	command.SetArgs(nil)
	if command.Execute() != nil || calls != 1 {
		t.Fatal("fixed helper refused")
	}
}
