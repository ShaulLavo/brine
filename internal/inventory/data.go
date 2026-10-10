package inventory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
)

const MappingProbeImage = "docker.io/library/python@sha256:2d9aefe2fef018a7eb2c13064c89c71929800fd2e5dccdbf52ea5da5bb8d929a"

// ProbeDataMapping exercises real keep-id ownership with a pinned disposable
// probe image. It never mounts app data, credentials or state; refusal never
// repairs modes or changes ownership. The caller holds the host mutation lock.
func ProbeDataMapping(ctx context.Context, runner localexec.Runner, root data.RootEvidence, runtime data.RuntimeIdentity) (data.MappingEvidence, error) {
	evidence := data.MappingEvidence{Runtime: runtime, RunnerUID: uint32(os.Geteuid()), RunnerGID: uint32(os.Getegid()), Root: root.Root, Device: root.Device, Image: MappingProbeImage, ObservedAt: time.Now().UTC()}
	if runner == nil || runtime.Validate() != nil || !root.Admits(root.Root, 1) {
		return evidence, data.ErrInvalid
	}
	timeout, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	directory, err := os.MkdirTemp(string(root.Root), ".brine-mapping-")
	if err != nil {
		return evidence, err
	}
	defer func() { os.Remove(filepath.Join(directory, "probe")); os.Remove(directory) }()
	if err = os.Chmod(directory, 0700); err != nil {
		return evidence, err
	}
	hostFile := filepath.Join(directory, "probe")
	if err = os.WriteFile(hostFile, []byte("host"), 0600); err != nil {
		return evidence, err
	}
	uid := strconv.FormatUint(uint64(runtime.UID), 10)
	gid := strconv.FormatUint(uint64(runtime.GID), 10)
	// Fixed script; no request values or paths are interpolated into its source.
	script := `import os; p="/data/probe"; assert open(p).read()=="host"; assert os.getuid()==int(os.environ["BRINE_UID"]); assert os.getgid()==int(os.environ["BRINE_GID"]); os.unlink(p); f=open(p,"w"); f.write("container"); f.close(); assert os.stat(p).st_mode & 0o777 == 0o600; print("mapped")`
	output, err := runner.Run(timeout, "podman", "run", "--rm", "--network=none", "--pull=never", "--userns=keep-id:uid="+uid+",gid="+gid, "--user="+uid+":"+gid, "--umask=0077", "--env=BRINE_UID="+uid, "--env=BRINE_GID="+gid, "--volume="+directory+":/data:rw", MappingProbeImage, "python", "-c", script)
	if err != nil || strings.TrimSpace(output) != "mapped" {
		return evidence, fmt.Errorf("keep-id mapping probe refused")
	}
	if err = data.VerifyRunnerFile(hostFile); err != nil {
		return evidence, err
	}
	content, err := os.ReadFile(hostFile)
	if err != nil || string(content) != "container" {
		return evidence, data.ErrInvalid
	}
	if err = os.WriteFile(hostFile, []byte("verified"), 0600); err != nil {
		return evidence, err
	}
	evidence.KeepID = true
	evidence.PrivateModes = true
	evidence.HostReadWrite = true
	evidence.ContainerReadWrite = true
	return evidence, nil
}
