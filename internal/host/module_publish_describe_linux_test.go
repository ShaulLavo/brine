//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"golang.org/x/sys/unix"
)

type descriptorTestRunner struct {
	command  localexec.Command
	deadline time.Time
	output   localexec.Output
	err      error
}

type blockedDescriptorInput struct{}

func (blockedDescriptorInput) Read([]byte) (int, error) { return 0, syscall.EAGAIN }

func TestDescriptorReportsOnlyFixedInputRefusalStages(t *testing.T) {
	for _, input := range []struct {
		reader io.Reader
		stage  string
	}{{blockedDescriptorInput{}, "input_would_block"}, {strings.NewReader(""), "input_empty"}} {
		_, err := readModuleDescriptorInput(input.reader)
		var failure *moduleDescriptorFailure
		if !errors.As(err, &failure) || failure.stage != input.stage || !errors.Is(err, datapublish.ErrRecord) || strings.Contains(err.Error(), "EAGAIN") {
			t.Fatal("input refusal lacked fixed value-free classification", err)
		}
	}
}

func (r *descriptorTestRunner) RunInput(ctx context.Context, command localexec.Command) (localexec.Output, error) {
	r.command = command
	r.deadline, _ = ctx.Deadline()
	return r.output, r.err
}

func TestDescriptorPurposeKeepsActualNativeOutputAndFixedBounds(t *testing.T) {
	raw, err := os.ReadFile("../spacetimedb/testdata/descriptor-default-column.json")
	if err != nil {
		t.Fatal(err)
	}
	runner := &descriptorTestRunner{output: localexec.Output{Stdout: raw, Stderr: []byte("private diagnostic")}}
	started := time.Now()
	result, err := extractModuleDescriptor(context.Background(), runner)
	if err != nil || !bytes.Equal(result, raw) {
		t.Fatal("actual native descriptor changed", err)
	}
	command := runner.command
	if command.Path != engineNativePath || !slices.Equal(command.Args, []string{"extract-schema", moduleDescriptorArtifactPath, "--host-type", "wasm"}) || command.Dir != "/tmp" || len(command.Stdin) != 0 || command.OutputLimit != moduleDescriptorOutputLimit || runner.deadline.Before(started) || runner.deadline.After(started.Add(moduleDescriptorWait+time.Second)) {
		t.Fatal("descriptor purpose escaped fixed arguments or bounds")
	}
	if len(command.Env) != 0 {
		t.Fatal("descriptor supplied an override to the fixed guest environment")
	}
	raw[0] = 'x'
	schema, err := spacetimedb.DecodeDescriptorV10(result, spacetimedb.DefaultSchemaLimits())
	if err != nil || schema.SHA256() != "95ac506b0911df63146d79cdba646d1e059e122240c1f0349a34335e9749d07b" {
		t.Fatal("mutable native output changed retained metadata", err)
	}
}

func TestDescriptorPurposeRefusesRawDiagnosticsAndUnqualifiedOutput(t *testing.T) {
	for _, output := range []localexec.Output{{Stdout: []byte(`{"V10":null}`)}, {Stdout: []byte("private diagnostic")}, {Stdout: []byte(`{"V9":{}}`)}} {
		if _, err := extractModuleDescriptor(context.Background(), &descriptorTestRunner{output: output}); !errors.Is(err, datapublish.ErrRecord) {
			t.Fatal("unqualified native output accepted", err)
		}
	}
	for _, failure := range []error{context.DeadlineExceeded, localexec.ErrOutputLimit, errors.New("private diagnostic")} {
		if _, err := extractModuleDescriptor(context.Background(), &descriptorTestRunner{err: failure}); !errors.Is(err, datapublish.ErrRecord) {
			t.Fatal("native failure leaked diagnostics", err)
		}
	}
}

func TestDescriptorInputCannotSupplyAPathSchemaOrPermission(t *testing.T) {
	valid := `{"wasm_sha256":"` + strings.Repeat("a", 64) + `","program_wire_hash":"` + strings.Repeat("b", 64) + `"}`
	if _, err := readModuleDescriptorInput(strings.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		strings.TrimSuffix(valid, "}") + `,"path":"/data/module.wasm"}`,
		strings.TrimSuffix(valid, "}") + `,"schema_sha256":"` + strings.Repeat("c", 64) + `"}`,
		strings.TrimSuffix(valid, "}") + `,"permitted":true}`,
		strings.Replace(valid, `"wasm_sha256":"`, `"wasm_sha256":null,"wasm_sha256":"`, 1),
		strings.Repeat(" ", 1025),
	} {
		if _, err := readModuleDescriptorInput(strings.NewReader(raw)); !errors.Is(err, datapublish.ErrRecord) {
			t.Fatal("caller selected descriptor effect authority", err)
		}
	}
}

func descriptorMountFixture() string {
	return "1 0 0:1 / / ro - overlay overlay ro\n" +
		"2 1 0:2 / /input/module.wasm ro - ext4 artifact ro\n" +
		"3 1 0:3 / /brine ro - ext4 helper ro\n" +
		"4 1 0:4 / /proc rw - proc proc rw\n" +
		"5 1 0:5 / /sys ro - sysfs sysfs ro\n" +
		"6 5 0:6 / /sys/fs/cgroup ro - cgroup2 cgroup ro\n" +
		"7 1 0:7 / /tmp rw - tmpfs tmpfs rw\n"
}

func TestDescriptorMountProfileHasOnlyArtifactAndHelperBindings(t *testing.T) {
	valid := descriptorMountFixture()
	if !moduleDescriptorMounts([]byte(valid)) {
		t.Fatal("exact descriptor mount profile refused")
	}
	for _, raw := range []string{
		strings.Replace(valid, "/input/module.wasm ro", "/input/module.wasm rw", 1),
		strings.Replace(valid, "/brine ro", "/brine rw,ro", 1),
		strings.Replace(valid, "2 1 0:2 / /input/module.wasm ro - ext4 artifact ro\n", "", 1),
		valid + "8 1 0:8 / /data rw - ext4 source rw\n",
		valid + "8 1 0:8 / /keys/private.pem ro - ext4 key ro\n",
		valid + "8 1 0:8 / /home/brine ro - ext4 home ro\n",
		valid + "8 1 0:8 / /input/module.wasm ro - ext4 extra ro\n",
		strings.Repeat(valid, 400),
	} {
		if moduleDescriptorMounts([]byte(raw)) {
			t.Fatal("writable, missing, duplicate or extra binding accepted")
		}
	}
	if err := RunModuleDescriptor(context.Background(), strings.NewReader("{}"), &bytes.Buffer{}); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("ordinary host acquired descriptor container authority", err)
	}
}

func TestModuleDescriptorNativeFailuresHaveClosedCodes(t *testing.T) {
	cases := []struct {
		err    error
		output []byte
		code   string
	}{
		{context.DeadlineExceeded, nil, "native_timeout"},
		{context.Canceled, nil, "native_cancelled"},
		{localexec.ErrOutputLimit, nil, "native_output_limit"},
		{&localexec.Error{Kind: localexec.NotFound}, nil, "native_missing"},
		{errors.New("private process diagnostic"), nil, "native_launch"},
		{nil, nil, "native_output_empty"},
		{nil, []byte("{}"), "native_output_shape"},
		{nil, bytes.Repeat([]byte("x"), moduleDescriptorOutputLimit+1), "native_output_limit"},
	}
	for _, value := range cases {
		t.Run(value.code, func(t *testing.T) {
			testModuleDescriptorNativeFailure(t, value.err, value.output, value.code)
		})
	}
	for _, mode := range []string{"exit", "arguments", "signal"} {
		t.Run(mode, func(t *testing.T) { testModuleDescriptorNativeExit(t, mode) })
	}
}

func testModuleDescriptorNativeFailure(t *testing.T, cause error, output []byte, code string) {
	t.Helper()
	_, err := extractModuleDescriptor(context.Background(), &descriptorTestRunner{err: cause, output: localexec.Output{Stdout: output}})
	var failure *moduleDescriptorFailure
	if !errors.Is(err, datapublish.ErrRecord) || !errors.As(err, &failure) || failure.stage != code || strings.Contains(err.Error(), "private process diagnostic") {
		t.Fatal("native failure escaped closed classification", err)
	}
}

func testModuleDescriptorNativeExit(t *testing.T, mode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := (localexec.ExecRunner{}).RunInput(ctx, localexec.Command{Path: os.Args[0], Args: []string{"-test.run=^TestModuleDescriptorNativeExitHelper$", "--", "BRINE_DESCRIPTOR_NATIVE_EXIT=" + mode}, Dir: t.TempDir(), OutputLimit: 1024})
	code := map[string]string{"exit": "native_exit", "arguments": "native_arguments", "signal": "native_signal"}[mode]
	if err == nil || moduleDescriptorNativeFailure(err, out.Stderr) != code {
		t.Fatal("real native child status classified incorrectly", mode, err)
	}
}

func TestModuleDescriptorNativeExitHelper(t *testing.T) {
	if !strings.HasPrefix(os.Args[len(os.Args)-1], "BRINE_DESCRIPTOR_NATIVE_EXIT=") {
		return
	}
	mode := strings.TrimPrefix(os.Args[len(os.Args)-1], "BRINE_DESCRIPTOR_NATIVE_EXIT=")
	if mode == "arguments" {
		_, _ = fmt.Fprintln(os.Stderr, "error: unexpected argument private diagnostic")
	}
	if mode == "signal" {
		_ = unix.Kill(os.Getpid(), unix.SIGTERM)
	}
	os.Exit(3)
}
