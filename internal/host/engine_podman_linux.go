//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type engineMount struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          *bool  `json:"RW"`
}

type engineContainer struct {
	ID           string   `json:"Id"`
	Name         string   `json:"Name"`
	Image        string   `json:"Image"`
	Path         string   `json:"Path"`
	Args         []string `json:"Args"`
	BoundingCaps []string `json:"BoundingCaps"`
	State        struct {
		Running    *bool  `json:"Running"`
		PID        *int   `json:"Pid"`
		CgroupPath string `json:"CgroupPath"`
	} `json:"State"`
	Config struct {
		Labels      map[string]string `json:"Labels"`
		User        string            `json:"User"`
		StopSignal  string            `json:"StopSignal"`
		StopTimeout *int              `json:"StopTimeout"`
		Umask       string            `json:"Umask"`
	} `json:"Config"`
	HostConfig struct {
		NetworkMode    string   `json:"NetworkMode"`
		ReadonlyRootfs *bool    `json:"ReadonlyRootfs"`
		Privileged     *bool    `json:"Privileged"`
		SecurityOpt    []string `json:"SecurityOpt"`
		CapAdd         []string `json:"CapAdd"`
		PidMode        string   `json:"PidMode"`
		CgroupManager  string   `json:"CgroupManager"`
		CgroupMode     string   `json:"CgroupMode"`
		Memory         *int64   `json:"Memory"`
		MemorySwap     *int64   `json:"MemorySwap"`
		PidsLimit      *int64   `json:"PidsLimit"`
		NanoCpus       *int64   `json:"NanoCpus"`
	} `json:"HostConfig"`
	Mounts []engineMount `json:"Mounts"`
}

func listEngineContainers(ctx context.Context, session localexec.Session) ([]engineContainer, error) {
	listed, err := session.Execute(ctx, "/usr/bin/podman", []string{"ps", "--all", "--quiet", "--no-trunc"}, nil, false)
	if err != nil {
		return nil, errEngineSource
	}
	ids := strings.Fields(listed.Stdout)
	if len(ids) > 128 {
		return nil, errEngineSource
	}
	if len(ids) == 0 {
		return []engineContainer{}, nil
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !data.ValidCatalogHash(id) || seen[id] {
			return nil, errEngineSource
		}
		seen[id] = true
	}
	args := append([]string{"container", "inspect"}, ids...)
	result, err := session.Execute(ctx, "/usr/bin/podman", args, nil, false)
	if err != nil {
		return nil, errEngineSource
	}
	var containers []engineContainer
	if json.Unmarshal([]byte(result.Stdout), &containers) != nil || len(containers) != len(ids) {
		return nil, errEngineSource
	}
	observed := map[string]bool{}
	for _, container := range containers {
		if !seen[container.ID] || observed[container.ID] || container.State.Running == nil || container.State.PID == nil || *container.State.PID < 0 || container.Mounts == nil || container.Config.Labels == nil {
			return nil, errEngineSource
		}
		observed[container.ID] = true
	}
	return containers, nil
}

func engineSourceAbsent(containers []engineContainer, source string) bool {
	for _, container := range containers {
		if !*container.State.Running && *container.State.PID == 0 {
			continue
		}
		for _, mount := range container.Mounts {
			if !engineMountPath(mount.Source) || data.OverlappingPaths(mount.Source, source) {
				return false
			}
		}
	}
	return true
}

func engineBindingAbsent(containers []engineContainer, permit store.EnginePermit) bool {
	for _, container := range containers {
		if container.Config.Labels["BRINE_ENGINE_BINDING"] == string(permit.Runtime.BindingID) && (*container.State.Running || *container.State.PID != 0) {
			return false
		}
	}
	return engineSourceAbsent(containers, permit.Reserved.Database.SourcePath())
}

func engineContainerName(binding data.ReplicaBindingID) string {
	return "brine-engine-" + string(binding)
}

func liveEngineArguments(permit store.EnginePermit, material engineMaterial) ([]string, error) {
	r := permit.Runtime
	source := permit.Reserved.Database.SourcePath()
	if r.Validate() != nil || !engineMountPath(source) || !engineMountPath(material.privateKey) || !engineMountPath(material.publicKey) {
		return nil, errEngineSource
	}
	args := engineContainerBase(r, engineContainerName(r.BindingID), "host")
	args = append(args, "--label", "PODMAN_SYSTEMD_UNIT="+engineContainerName(r.BindingID)+".service", "--volume", source+":/data:rw", "--volume", material.privateKey+":/keys/private.pem:ro", "--volume", material.publicKey+":/keys/public.pem:ro", "--entrypoint", engineNativePath, r.Image)
	return append(args, engineNativeArguments(r, r.HostPort)...), nil
}

func privateEngineArguments(permit store.EnginePermit, material engineMaterial, copy *engineSourceCopy, nonce string, binary string) ([]string, error) {
	r := permit.Runtime
	if r.Validate() != nil || !data.ValidID(nonce) || !engineMountPath(copy.path) || data.OverlappingPaths(copy.path, permit.Reserved.Database.SourcePath()) || !engineMountPath(binary) || !engineMountPath(material.privateKey) || !engineMountPath(material.publicKey) {
		return nil, errEngineSource
	}
	name := "brine-engine-verify-" + string(r.BindingID) + "-" + nonce
	args := engineContainerBase(r, name, "none")
	return append(args, "--interactive", "--label", "BRINE_PRIVATE_VERIFY="+nonce, "--volume", copy.path+":/data:rw", "--volume", material.privateKey+":/keys/private.pem:ro", "--volume", material.publicKey+":/keys/public.pem:ro", "--volume", binary+":/brine:ro", "--entrypoint", "/brine", r.Image, "host", "engine-copy-observe"), nil
}

func engineContainerBase(runtime data.EngineRuntime, name, network string) []string {
	return []string{"run", "--name", name, "--label", "BRINE_ENGINE_BINDING=" + string(runtime.BindingID), "--network", network, "--sdnotify", "ignore", "--read-only", "--userns", "keep-id:uid=1000,gid=1000", "--user", "1000:1000", "--umask", "0077", "--memory", strconv.Itoa(runtime.MemoryMB) + "m", "--pids-limit", strconv.Itoa(runtime.PIDsLimit), "--cpus", "1", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--stop-signal", "SIGINT", "--stop-timeout", strconv.Itoa(runtime.StopSeconds), "--env", "TOKIO_WORKER_THREADS=2", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec", "--log-driver", "none"}
}

func privateEngineDisposed(ctx context.Context, session localexec.Session, nonce string) error {
	containers, err := listEngineContainers(ctx, session)
	if err != nil {
		return err
	}
	for _, container := range containers {
		if container.Config.Labels["BRINE_PRIVATE_VERIFY"] != nonce {
			continue
		}
		if *container.State.Running || *container.State.PID != 0 {
			return errEngineSource
		}
		if _, err = session.Execute(ctx, "/usr/bin/podman", []string{"container", "rm", container.ID}, nil, true); err != nil {
			return errEngineSource
		}
	}
	return nil
}

func removeStoppedEngine(ctx context.Context, session localexec.Session, permit store.EnginePermit) error {
	containers, err := listEngineContainers(ctx, session)
	if err != nil {
		return err
	}
	name := engineContainerName(permit.Runtime.BindingID)
	for _, container := range containers {
		if container.Name != name {
			continue
		}
		if container.Config.Labels["PODMAN_SYSTEMD_UNIT"] != name+".service" || container.Image != permit.Runtime.ImageID || *container.State.Running || *container.State.PID != 0 {
			return errEngineSource
		}
		if _, err = session.Execute(ctx, "/usr/bin/podman", []string{"container", "rm", container.ID}, nil, true); err != nil {
			return errEngineSource
		}
	}
	return nil
}

func currentEngineStarterAlone(selfPID uint32, cgroup string) bool {
	if !filepath.IsAbs(cgroup) || filepath.Clean(cgroup) != cgroup || cgroup == "/" {
		return false
	}
	root, err := os.OpenRoot("/sys/fs/cgroup")
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	return engineStarterCgroupOnly(root, strings.TrimPrefix(cgroup, "/"), selfPID)
}

func engineStarterCgroupOnly(root *os.Root, path string, selfPID uint32) bool {
	paths := []string{path}
	self := strconv.FormatUint(uint64(selfPID), 10)
	found := false
	for i := 0; i < len(paths); i++ {
		if len(paths) > 128 || strings.Count(paths[i], "/") > 64 {
			return false
		}
		pids, err := engineCgroupPIDs(root, paths[i])
		if err != nil || len(pids) > 1 || len(pids) == 1 && (pids[0] != self || found) {
			return false
		}
		found = found || len(pids) == 1
		children, err := engineCgroupChildren(root, paths[i])
		if err != nil {
			return false
		}
		paths = append(paths, children...)
	}
	return found
}

func engineCgroupPIDs(root *os.Root, path string) ([]string, error) {
	file, err := root.Open(filepath.Join(path, "cgroup.procs"))
	if err != nil {
		return nil, errEngineSource
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return nil, errEngineSource
	}
	return strings.Fields(string(raw)), nil
}

func engineCgroupChildren(root *os.Root, path string) ([]string, error) {
	directory, err := root.Open(path)
	if err != nil {
		return nil, errEngineSource
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(257)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) == 257 {
		return nil, errEngineSource
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			paths = append(paths, filepath.Join(path, entry.Name()))
		}
	}
	return paths, nil
}

func verifyEngineLoadedService(ctx context.Context, session localexec.Session, home string, unit systemd.Unit) error {
	result, err := session.Execute(ctx, "/usr/bin/systemctl", []string{"--user", "show", unit.String(), "--all", "--property=FragmentPath,DropInPaths,NeedDaemonReload"}, nil, false)
	if err != nil {
		return errEngineSource
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key != "FragmentPath" && key != "DropInPaths" && key != "NeedDaemonReload" {
			return errEngineSource
		}
		if _, duplicate := fields[key]; duplicate {
			return errEngineSource
		}
		fields[key] = value
	}
	want := filepath.Join(home, ".config/systemd/user", unit.String())
	if len(fields) != 3 || fields["FragmentPath"] != want || fields["DropInPaths"] != "" || fields["NeedDaemonReload"] != "no" {
		return errEngineSource
	}
	return nil
}

func ownedLiveEngine(container engineContainer, permit store.EnginePermit, material engineMaterial) bool {
	name := engineContainerName(permit.Runtime.BindingID)
	if container.Name != name || container.Config.Labels["PODMAN_SYSTEMD_UNIT"] != name+".service" || container.Image != permit.Runtime.ImageID || container.State.PID == nil || container.State.Running == nil || container.Path != engineNativePath || !slices.Equal(container.Args, engineNativeArguments(permit.Runtime, permit.Runtime.HostPort)) || !engineContainerSecurity(container, permit.Runtime, "host") {
		return false
	}
	return engineContainerMounts(container.Mounts, map[string]engineMount{
		"/data":             {Source: permit.Reserved.Database.SourcePath(), Type: "bind", RW: engineBool(true)},
		"/keys/private.pem": {Source: material.privateKey, Type: "bind", RW: engineBool(false)},
		"/keys/public.pem":  {Source: material.publicKey, Type: "bind", RW: engineBool(false)},
	})
}

func (r *EngineStartupRuntime) waitEngineHandoff(ctx context.Context, permit store.EnginePermit, material engineMaterial, exited <-chan error) (*engineNativeHandle, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		containers, err := listEngineContainers(ctx, r.session)
		if err != nil {
			return nil, err
		}
		for _, container := range containers {
			if container.Name != engineContainerName(permit.Runtime.BindingID) || !*container.State.Running {
				continue
			}
			return bindEngineNative(container, permit, material)
		}
		select {
		case <-exited:
			return nil, errEngineSource
		case <-ctx.Done():
			return nil, errEngineSource
		case <-ticker.C:
		}
	}
}

func (r *EngineStartupRuntime) stopLiveEngine(ctx context.Context, permit store.EnginePermit) error {
	stop, cancel := context.WithTimeout(ctx, time.Duration(permit.Runtime.StopSeconds+5)*time.Second)
	defer cancel()
	containers, err := listEngineContainers(stop, r.session)
	if err != nil {
		return err
	}
	for _, container := range containers {
		if container.Name != engineContainerName(permit.Runtime.BindingID) {
			continue
		}
		if !ownedLiveEngine(container, permit, engineMaterialPaths(r.stateRoot, permit.Runtime)) {
			return errEngineSource
		}
		if _, err = r.session.Execute(stop, "/usr/bin/podman", []string{"stop", "--time", strconv.Itoa(permit.Runtime.StopSeconds), container.ID}, nil, true); err != nil {
			return errEngineSource
		}
	}
	return r.liveEngineAbsent(stop, permit)
}

func (r *EngineStartupRuntime) liveEngineAbsent(ctx context.Context, permit store.EnginePermit) error {
	inspect, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	containers, err := listEngineContainers(inspect, r.session)
	if err != nil || !engineBindingAbsent(containers, permit) {
		return errEngineSource
	}
	return nil
}
