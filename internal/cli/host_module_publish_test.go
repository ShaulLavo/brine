package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestModuleDescriptorUsesOnlyItsFixedGuestPurpose(t *testing.T) {
	input := strings.NewReader("private request")
	var output bytes.Buffer
	called := 0
	deps := Dependencies{Context: context.Background(), Stdin: input, Stdout: &output, Stderr: &bytes.Buffer{}, HostModuleDescribe: func(ctx context.Context, reader io.Reader, writer io.Writer) error {
		called++
		if ctx == nil || reader != input {
			t.Fatal("guest input changed")
		}
		_, err := io.WriteString(writer, "fixed descriptor")
		return err
	}}
	lifecycle := RuntimeLifecycle{Open: func(context.Context, bool) (RuntimeServices, error) {
		t.Fatal("descriptor opened deployment runtime")
		return RuntimeServices{}, nil
	}, OpenEngineStartup: func(context.Context) (RuntimeServices, error) {
		t.Fatal("descriptor opened live engine state")
		return RuntimeServices{}, nil
	}}
	if err := ExecuteWithRuntime(deps, []string{"host", "module-describe"}, lifecycle); err != nil || called != 1 || output.String() != "fixed descriptor" {
		t.Fatal("descriptor guest purpose failed", err, called)
	}
}

func TestModuleDescriptorCannotAcceptCallerPathsOrFlags(t *testing.T) {
	for _, args := range [][]string{{"host", "module-describe", "/data/module.wasm"}, {"host", "module-describe", "--image", "untrusted"}, {"host", "module-describe", "--help"}} {
		deps := Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, HostModuleDescribe: func(context.Context, io.Reader, io.Writer) error {
			t.Fatal("invalid descriptor command had effects")
			return nil
		}}
		_ = Execute(deps, args)
	}
}
