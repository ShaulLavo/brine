//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

func engineStartupFixture(t *testing.T) (store.EnginePermit, engineMaterial) {
	t.Helper()
	binding, _ := engineSourceFixture(t)
	secret := func(ref string) data.EngineSecretVersion {
		return data.EngineSecretVersion{Reference: ref, Version: 1, SHA256: strings.Repeat("a", 64), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	}
	runtime := data.EngineRuntime{BindingID: binding.BindingID, EpochID: data.ReplicaEpochID(strings.Repeat("d", 32)), State: data.EngineInitialized, Image: data.SpacetimeDBImage, ManifestDigest: data.SpacetimeDBAMD64Manifest, ImageID: strings.Repeat("a", 64), Version: "2.11.0", Runtime: data.RuntimeIdentity{UID: 1000, GID: 1000}, HostPort: 23111, Database: data.NativeDatabaseIdentity(strings.Repeat("e", 64)), Publisher: data.NativePublisherIdentity(strings.Repeat("f", 64)), Names: []string{"game"}, Module: data.EngineModuleDefinition{Program: data.NativeProgramWireHash(strings.Repeat("b", 64)), Wasm: data.WasmSHA256(strings.Repeat("c", 64)), Schema: data.EngineSchemaDefinition{Marker: "game-v1", Canonicalizer: spacetimedb.CanonicalizerVersion, SHA256: strings.Repeat("d", 64)}}, Secrets: data.EngineSecrets{Publisher: secret("publisher"), SigningPrivate: secret("signing-private"), SigningPublic: secret("signing-public"), AuthPolicy: secret("auth-policy")}, MemoryMB: 256, PIDsLimit: 128, PagePoolMB: 64, StopSeconds: 10, StartupSeconds: 60, CopyBytes: 1 << 20, CopyFiles: 128, UnitSHA256: strings.Repeat("a", 64)}
	return store.EnginePermit{Reserved: store.ReservedEngine{Database: binding}, Runtime: runtime}, engineMaterial{privateKey: "/private/v1.secret", publicKey: "/public/v1.secret"}
}

func ownedEngineFixture(t *testing.T) (engineContainer, store.EnginePermit, engineMaterial) {
	t.Helper()
	permit, material := engineStartupFixture(t)
	c := engineContainer{ID: strings.Repeat("c", 64), Image: permit.Runtime.ImageID, Name: engineContainerName(permit.Runtime.BindingID), Path: engineNativePath, Args: engineNativeArguments(permit.Runtime, permit.Runtime.HostPort)}
	c.Config.Labels = map[string]string{"PODMAN_SYSTEMD_UNIT": c.Name + ".service"}
	c.Config.User, c.Config.StopSignal = "1000:1000", "SIGINT"
	c.Config.Umask = "0077"
	c.Config.StopTimeout = &permit.Runtime.StopSeconds
	c.State.Running, c.State.PID = engineBool(true), new(int)
	*c.State.PID = 123
	c.HostConfig.NetworkMode, c.HostConfig.CgroupManager, c.HostConfig.CgroupMode, c.HostConfig.PidMode = "host", "systemd", "private", "private"
	c.HostConfig.ReadonlyRootfs, c.HostConfig.Privileged = engineBool(true), engineBool(false)
	c.HostConfig.SecurityOpt = []string{"no-new-privileges"}
	mem, pids, cpus := int64(permit.Runtime.MemoryMB)<<20, int64(permit.Runtime.PIDsLimit), int64(1_000_000_000)
	c.HostConfig.Memory, c.HostConfig.PidsLimit, c.HostConfig.NanoCpus = &mem, &pids, &cpus
	c.Mounts = []engineMount{{Source: permit.Reserved.Database.SourcePath(), Destination: "/data", Type: "bind", RW: engineBool(true)}, {Source: material.privateKey, Destination: "/keys/private.pem", Type: "bind", RW: engineBool(false)}, {Source: material.publicKey, Destination: "/keys/public.pem", Type: "bind", RW: engineBool(false)}}
	return c, permit, material
}

func TestEngineNativeHandoffRejectsForeignMountsAndSecurity(t *testing.T) {
	for _, attack := range []string{"extra-mount", "wrong-source", "key-writable", "image", "argv", "user", "network", "pid-namespace", "root-writable", "privileged", "capabilities", "missing-resource", "wrong-stop-signal"} {
		t.Run(attack, func(t *testing.T) { testEngineHandoffAttack(t, attack) })
	}
}

func testEngineHandoffAttack(t *testing.T, attack string) {
	t.Helper()
	c, permit, material := ownedEngineFixture(t)
	if !ownedLiveEngine(c, permit, material) {
		t.Fatal("exact fixed runtime refused")
	}
	switch attack {
	case "extra-mount":
		c.Mounts = append(c.Mounts, engineMount{Source: "/home/other", Destination: "/extra", Type: "bind", RW: engineBool(false)})
	case "wrong-source":
		c.Mounts[0].Source = "/foreign"
	case "key-writable":
		c.Mounts[1].RW = engineBool(true)
	case "image":
		c.Image = strings.Repeat("b", 64)
	case "argv":
		c.Args = append(c.Args, "--listen-addr", "0.0.0.0:23111")
	case "user":
		c.Config.User = "0:0"
	case "network":
		c.HostConfig.NetworkMode = "bridge"
	case "pid-namespace":
		c.HostConfig.PidMode = "host"
	case "root-writable":
		c.HostConfig.ReadonlyRootfs = engineBool(false)
	case "privileged":
		c.HostConfig.Privileged = engineBool(true)
	case "capabilities":
		c.BoundingCaps = []string{"CAP_SYS_ADMIN"}
	case "missing-resource":
		c.HostConfig.Memory = nil
	case "wrong-stop-signal":
		c.Config.StopSignal = "SIGTERM"
	}
	if ownedLiveEngine(c, permit, material) {
		t.Fatal("unowned native launch accepted", attack)
	}
}

func TestEngineStarterRejectsHiddenCgroupDescendant(t *testing.T) {
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "unit", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "unit", "cgroup.procs"), []byte("123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(directory, "unit", "nested", "cgroup.procs")
	if err := os.WriteFile(child, nil, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if !engineStarterCgroupOnly(root, "unit", 123) {
		t.Fatal("exact main process refused")
	}
	if err = os.WriteFile(child, []byte("456\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if engineStarterCgroupOnly(root, "unit", 123) {
		t.Fatal("foreign descendant escaped direct cgroup inspection")
	}
}

func TestEngineGuardRejectsWrongAndReleasedCapabilities(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil { //nolint:gosec // Private directory or deliberately unsafe-mode refusal fixture.
		t.Fatal(err)
	}
	binding := data.ReplicaBindingID(strings.Repeat("a", 32))
	if err := data.PrepareEngineLocks(context.Background(), root, binding); err != nil {
		t.Fatal(err)
	}
	g, err := acquireEngineStart(context.Background(), root, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.close() }()
	if !g.active() {
		t.Fatal("held guards refused")
	}
	g.binding = data.ReplicaBindingID(strings.Repeat("b", 32))
	if g.active() {
		t.Fatal("another binding consumed guards")
	}
	g.binding = binding
	if err = g.launch.Release(); err != nil {
		t.Fatal(err)
	}
	if g.active() {
		t.Fatal("released launch guard retained authority")
	}
	if _, err = data.AcquireEngineLifetimeLock(context.Background(), root, binding); !errors.Is(err, data.ErrEngineLocked) {
		t.Fatal("handoff released runtime lifetime", err)
	}
	var empty data.EngineLock
	if err = empty.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestEngineGuardRejectsPathReplacementBeforeHandoff(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil { //nolint:gosec // Private directory or deliberately unsafe-mode refusal fixture.
		t.Fatal(err)
	}
	binding := data.ReplicaBindingID(strings.Repeat("a", 32))
	if err := data.PrepareEngineLocks(context.Background(), root, binding); err != nil {
		t.Fatal(err)
	}
	g, err := acquireEngineStart(context.Background(), root, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.close() }()
	path := filepath.Join(root, "engine-locks", string(binding)+".launch")
	if err = os.Rename(path, path+".preserved"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if g.active() {
		t.Fatal("replaced lock retained source-proof authority")
	}
}

type engineInspectRunner struct {
	results  []localexec.Result
	commands []localexec.Command
}

func (r *engineInspectRunner) Execute(_ context.Context, command localexec.Command) (localexec.Result, error) {
	r.commands = append(r.commands, command)
	if len(r.results) == 0 {
		return localexec.Result{}, errors.New("unexpected effect")
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result, nil
}

func TestEngineUnknownPrivateDisposalNeverRemovesLiveContainer(t *testing.T) {
	c, _, _ := ownedEngineFixture(t)
	nonce := strings.Repeat("a", 32)
	c.Config.Labels["BRINE_PRIVATE_VERIFY"] = nonce
	raw, err := json.Marshal([]engineContainer{c})
	if err != nil {
		t.Fatal(err)
	}
	runner := &engineInspectRunner{results: []localexec.Result{{Stdout: c.ID + "\n"}, {Stdout: string(raw)}}}
	session, err := localexec.NewSession(runner, 1000, "/home/fixture", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if privateEngineDisposed(context.Background(), session, nonce) == nil {
		t.Fatal("running verifier classified disposed")
	}
	for _, command := range runner.commands {
		if command.Mutation {
			t.Fatal("uncertain verifier was removed or retried")
		}
	}
}

func TestEngineNativePidfdTracksActualLifetime(t *testing.T) {
	command := exec.Command("/bin/sleep", "30")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	process := "/proc/" + strconv.Itoa(command.Process.Pid)
	raw, err := engineProcessFile(process, "stat")
	pid, ticks, parseErr := parseEngineProcessStat(string(raw))
	if err != nil || parseErr != nil {
		t.Fatal(err, parseErr)
	}
	raw, err = engineProcessFile(process, "cgroup")
	group, parseErr := parseEngineCgroup(string(raw))
	if err != nil || parseErr != nil {
		t.Fatal(err, parseErr)
	}
	fd, err := unix.PidfdOpen(command.Process.Pid, unix.PIDFD_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	handle := engineNativeHandle{pid: pid, ticks: ticks, cgroup: group, fd: fd}
	defer handle.close()
	if handle.exited() || handle.alive() != nil {
		t.Fatal("live process identity refused")
	}
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	if !handle.exited() || handle.alive() == nil {
		t.Fatal("exited native identity remained live")
	}
}

func TestEngineRuntimeDecoderRejectsIncompleteOwnership(t *testing.T) {
	permit, _ := engineStartupFixture(t)
	raw, err := json.Marshal(permit.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	var decoded data.EngineRuntime
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, attack := range []string{
		`null`, `{}`, string(raw) + `{}`,
		strings.Replace(string(raw), `"runtime":{"uid":1000,"gid":1000}`, `"runtime":null`, 1),
		strings.Replace(string(raw), `"uid":1000`, `"uid":0`, 1),
		strings.Replace(string(raw), `"names":["game"]`, `"names":null`, 1),
		strings.Replace(string(raw), `"names":["game"]`, `"names":[null]`, 1),
		strings.Replace(string(raw), `"names":["game"]`, `"names":["game","game"]`, 1),
		strings.Replace(string(raw), `"copy_files":128`, `"copy_files":null`, 1),
		strings.Replace(string(raw), `"binding_id":`, `"unknown":1,"binding_id":`, 1),
		strings.Replace(string(raw), `"binding_id":`, `"binding_id":"bad","binding_id":`, 1),
	} {
		if json.Unmarshal([]byte(attack), &decoded) == nil {
			t.Fatal("malformed runtime admitted")
		}
	}
	clone := permit.Runtime.Clone()
	clone.Names[0] = "changed"
	if permit.Runtime.Names[0] != "game" {
		t.Fatal("borrowed names changed committed runtime")
	}
}

func TestEngineSecretDriftAndUnsafeMetadataRefuse(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil { //nolint:gosec // Private directory or deliberately unsafe-mode refusal fixture.
		t.Fatal(err)
	}
	content := []byte("synthetic-private-material")
	sum := sha256.Sum256(content)
	file := filepath.Join(directory, "v1.secret")
	if err := os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err = readEnginePrivateFile(context.Background(), root, "v1.secret", hex.EncodeToString(sum[:]), 128); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, []byte("changed-private-material"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readEnginePrivateFile(context.Background(), root, "v1.secret", hex.EncodeToString(sum[:]), 128); err == nil {
		t.Fatal("changed signing material admitted")
	}
	if err = os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(file, 0644); err != nil { //nolint:gosec // Private directory or deliberately unsafe-mode refusal fixture.
		t.Fatal(err)
	}
	if _, err = readEnginePrivateFile(context.Background(), root, "v1.secret", hex.EncodeToString(sum[:]), 128); err == nil {
		t.Fatal("public signing material file admitted")
	}
}

func TestEngineMaterialVersionsAreImmutableAndPrivate(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil { //nolint:gosec // Private directory or deliberately unsafe-mode refusal fixture.
		t.Fatal(err)
	}
	root, err := engineOpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err = engineMakePrivateDirectories(root, "engine-secrets/binding/publisher"); err != nil {
		t.Fatal(err)
	}
	content := []byte("synthetic-private-token")
	sum := sha256.Sum256(content)
	name := "engine-secrets/binding/publisher/v1.secret"
	hash := hex.EncodeToString(sum[:])
	for i := 0; i < 2; i++ {
		if err = engineWritePrivateVersion(context.Background(), root, name, hash, content); err != nil {
			t.Fatal("idempotent immutable delivery failed", err)
		}
	}
	changed := []byte("different-private-token")
	sum = sha256.Sum256(changed)
	if engineWritePrivateVersion(context.Background(), root, name, hex.EncodeToString(sum[:]), changed) == nil {
		t.Fatal("immutable material overwritten")
	}
	material, err := NewEngineSecretMaterial(content, []byte("synthetic-private-key"), []byte("synthetic-public-key"), []byte(`{"mode":"supplied_key_jwt","require_client_token":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer material.Clear()
	if _, err = json.Marshal(material); err == nil {
		t.Fatal("private material serialized")
	}
	if strings.Contains(fmt.Sprintf("%v %#v", material, material), string(content)) {
		t.Fatal("private material printed")
	}
}

func TestEnginePrivateMountContainmentRejectsExtraAndWritableKey(t *testing.T) {
	var lines []string
	for i, path := range []string{"/", "/data", "/keys/private.pem", "/keys/public.pem", "/brine", "/proc", "/sys", "/sys/fs/cgroup", "/tmp"} {
		mode := "ro"
		if path == "/data" || path == "/proc" || path == "/tmp" {
			mode = "rw"
		}
		lines = append(lines, fmt.Sprintf("%d 0 0:1 / %s %s - tmpfs fixture %s", i+1, path, mode, mode))
	}
	raw := strings.Join(lines, "\n")
	if !engineReplayMounts([]byte(raw)) {
		t.Fatal("fixed mount namespace refused")
	}
	for _, attack := range []string{raw + "\n20 0 0:1 / /live rw - ext4 fixture rw", strings.Replace(raw, "/keys/private.pem ro", "/keys/private.pem rw", 1), raw + "\n20 0 0:1 / /run/notify rw - tmpfs fixture rw"} {
		if engineReplayMounts([]byte(attack)) {
			t.Fatal("private namespace admitted external mounts or writable key")
		}
	}
}

func TestEngineHandoffStillBindsAllocatedDirectory(t *testing.T) {
	permit, _ := engineStartupFixture(t)
	root, err := engineOpenRoot(permit.Reserved.Database.SourcePath())
	if err != nil {
		t.Fatal(err)
	}
	file, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	member, err := engineMember(file, ".")
	_ = file.Close()
	_ = root.Close()
	if err != nil {
		t.Fatal(err)
	}
	permit.Allocation = &data.AllocationReceipt{DirectoryDevice: member.Device, DirectoryInode: member.Inode}
	if !engineAllocatedSource(permit) {
		t.Fatal("exact allocated source refused")
	}
	path := permit.Reserved.Database.SourcePath()
	if err = os.Rename(path, path+"-preserved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if engineAllocatedSource(permit) {
		t.Fatal("replacement source inode earned live handoff")
	}
}

func TestEngineServicePrivateUnitUnderTraversableHome(t *testing.T) {
	permit, _ := engineStartupFixture(t)
	home := t.TempDir()
	if err := os.Chmod(home, 0755); err != nil {
		t.Fatal(err)
	} //nolint:gosec // Standard enrolled home is traversable; the unit root stays private.
	directory := filepath.Join(home, ".config/systemd/user")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := EngineServiceBytes(permit.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	permit.Runtime.UnitSHA256 = hex.EncodeToString(sum[:])
	name := filepath.Join(directory, engineContainerName(permit.Runtime.BindingID)+".service")
	if err = os.WriteFile(name, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = verifyEngineService(context.Background(), home, permit); err != nil {
		t.Fatal("standard home blocked private owned service", err)
	}
	if err = os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	} //nolint:gosec // Deliberately unsafe private unit-root mode.
	if verifyEngineService(context.Background(), home, permit) == nil {
		t.Fatal("public unit root admitted")
	}
}
