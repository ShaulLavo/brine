//go:build linux

package host

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"golang.org/x/sys/unix"
)

const moduleDescriptorArtifactPath = "/input/module.wasm"
const moduleDescriptorWait = 15 * time.Second
const moduleDescriptorOutputLimit = 1 << 20

type moduleDescriptorInput struct {
	WasmSHA256      string `json:"wasm_sha256"`
	ProgramWireHash string `json:"program_wire_hash"`
}

// RunModuleDescriptor is a fixed private-container purpose. It refuses execution
// on the ordinary host or a sandbox exposing source data or credentials.
func RunModuleDescriptor(ctx context.Context, input io.Reader, output io.Writer) error {
	if err := moduleDescriptorContained(); err != nil {
		return err
	}
	request, err := readModuleDescriptorInput(input)
	if err != nil {
		return err
	}
	artifact, err := readModuleDescriptorArtifact()
	if err != nil || string(artifact.WasmSHA256()) != request.WasmSHA256 || string(artifact.ProgramWireHash()) != request.ProgramWireHash {
		return datapublish.ErrRecord
	}
	raw, err := extractModuleDescriptor(ctx, localexec.ExecRunner{})
	if err != nil {
		return err
	}
	current, err := readModuleDescriptorArtifact()
	if err != nil || current.WasmSHA256() != artifact.WasmSHA256() || current.ProgramWireHash() != artifact.ProgramWireHash() {
		return datapublish.ErrRecord
	}
	written, err := output.Write(raw)
	if err == nil && written != len(raw) {
		return io.ErrShortWrite
	}
	return err
}

func readModuleDescriptorInput(input io.Reader) (moduleDescriptorInput, error) {
	raw, err := io.ReadAll(io.LimitReader(input, 1025))
	if err != nil || len(raw) == 0 || len(raw) > 1024 {
		return moduleDescriptorInput{}, datapublish.ErrRecord
	}
	if _, err := strictjson.Object(raw, "wasm_sha256", "program_wire_hash"); err != nil {
		return moduleDescriptorInput{}, datapublish.ErrRecord
	}
	var value moduleDescriptorInput
	if json.Unmarshal(raw, &value) != nil || !data.ValidCatalogHash(value.WasmSHA256) || !data.ValidCatalogHash(value.ProgramWireHash) {
		return moduleDescriptorInput{}, datapublish.ErrRecord
	}
	return value, nil
}

func readModuleDescriptorArtifact() (datapublish.Artifact, error) {
	fd, err := unix.Open(moduleDescriptorArtifactPath, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return datapublish.Artifact{}, datapublish.ErrRecord
	}
	file := os.NewFile(uintptr(fd), moduleDescriptorArtifactPath)
	defer func() { _ = file.Close() }()
	var info unix.Stat_t
	if unix.Fstat(fd, &info) != nil || info.Mode&unix.S_IFMT != unix.S_IFREG || info.Mode&07777 != 0600 || info.Uid != 1000 || info.Nlink != 1 || info.Size < 8 || info.Size > datapublish.ModuleArtifactLimit {
		return datapublish.Artifact{}, datapublish.ErrRecord
	}
	raw, err := io.ReadAll(io.LimitReader(file, datapublish.ModuleArtifactLimit+1))
	if err != nil || int64(len(raw)) != info.Size {
		return datapublish.Artifact{}, datapublish.ErrRecord
	}
	return datapublish.NewArtifact(raw)
}

func extractModuleDescriptor(ctx context.Context, runner localexec.InputRunner) ([]byte, error) {
	wait, cancel := context.WithTimeout(ctx, moduleDescriptorWait)
	defer cancel()
	result, err := runner.RunInput(wait, localexec.Command{Path: engineNativePath,
		Args: []string{"extract-schema", moduleDescriptorArtifactPath, "--host-type", "wasm"},
		Env:  []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp", "TOKIO_WORKER_THREADS=2"}, Dir: "/tmp", OutputLimit: moduleDescriptorOutputLimit})
	if err != nil {
		return nil, datapublish.ErrRecord
	}
	if _, err = spacetimedb.DecodeDescriptorV10(result.Stdout, spacetimedb.DefaultSchemaLimits()); err != nil {
		return nil, datapublish.ErrRecord
	}
	return slices.Clone(result.Stdout), nil
}

func moduleDescriptorContained() error {
	if os.Getpid() != 1 || os.Geteuid() != 1000 || os.Getegid() != 1000 {
		return datapublish.ErrRecord
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) != 1 || interfaces[0].Flags&net.FlagLoopback == 0 {
		return datapublish.ErrRecord
	}
	for _, path := range []string{"/data", "/keys", "/home/brine", "/work", "/run/podman/podman.sock", "/var/run/docker.sock"} {
		if _, err = os.Lstat(path); !os.IsNotExist(err) {
			return datapublish.ErrRecord
		}
	}
	raw, err := readModuleGuestMountInfo()
	if err != nil || !moduleDescriptorMounts(raw) {
		return datapublish.ErrRecord
	}
	status, err := engineProcessStatus("/proc/self")
	if err != nil || !engineProcessOwned(status) || !slices.Equal(strings.Fields(status["NSpid"]), []string{"1"}) {
		return datapublish.ErrRecord
	}
	return nil
}
