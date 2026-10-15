//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

const engineMountProfileFormat = "brine-engine-mount-profile-v1"
const engineMountProfileLimit = 8192
const engineProfileHelper = "/usr/local/bin/brine"

type engineProfileNode struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Mode   uint32 `json:"mode"`
	UID    uint32 `json:"uid"`
	GID    uint32 `json:"gid"`
	Size   int64  `json:"size"`
	Links  uint64 `json:"links"`
}
type engineProfileNamespace struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}
type engineMountProfile struct {
	Format         string                 `json:"format"`
	PrimaryTicks   uint64                 `json:"primary_ticks"`
	UserNamespace  engineProfileNamespace `json:"user_namespace"`
	MountNamespace engineProfileNamespace `json:"mount_namespace"`
	Helper         *engineProfileNode     `json:"helper"`
	Data           *engineProfileNode     `json:"data"`
	PrivateKey     *engineProfileNode     `json:"private_key"`
	PublicKey      *engineProfileNode     `json:"public_key"`
	Artifact       *engineProfileNode     `json:"artifact"`
}
type engineProfileIdentity struct {
	ticks       uint64
	user, mount engineProfileNamespace
	command     []byte
	executable  string
}

type engineProfileCommandMode uint8

const (
	engineProfileStableCommand engineProfileCommandMode = iota
	engineProfileStartingCommand
	engineProfileRunningCommand
)

func (mode engineProfileCommandMode) matches(host, guest engineMountProfile, mappings engineProfileMappings) bool {
	if mode != engineProfileRunningCommand {
		return engineProfileMatches(host, guest, mappings)
	}
	if !engineProfileNodeMatches(host.Helper, guest.Helper, mappings) && !engineReplacedHelperMatches(host.Helper, guest.Helper, mappings) {
		return false
	}
	host.Helper, guest.Helper = nil, nil
	return engineProfileMatches(host, guest, mappings)
}

func engineReplacedHelperMatches(host, guest *engineProfileNode, mappings engineProfileMappings) bool {
	// Atomic replacement unlinks the protected helper retained by the read-only bind.
	return host != nil && guest != nil && host.Mode == unix.S_IFREG|0755 && host.UID == 0 && host.GID == 0 && host.Links == 1 &&
		guest.Device == host.Device && guest.Inode != 0 && guest.Inode != host.Inode && guest.Mode == host.Mode &&
		guest.UID == mappings.uid.namespaceID(0) && guest.GID == mappings.gid.namespaceID(0) &&
		guest.Links == 0 && guest.Size > 0 && guest.Size <= 64<<20
}

func (p engineMountProfile) boundTo(identity engineProfileIdentity) bool {
	return p.PrimaryTicks == identity.ticks && p.UserNamespace == identity.user && p.MountNamespace == identity.mount
}

type engineProfileFiles struct {
	helper, data, privateKey, publicKey, artifact *os.File
}

func (f *engineProfileFiles) close() {
	for _, file := range []*os.File{f.helper, f.data, f.privateKey, f.publicKey, f.artifact} {
		if file != nil {
			_ = file.Close()
		}
	}
}

func engineProfileStat(info os.FileInfo) (engineProfileNode, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSymlink != 0 {
		return engineProfileNode{}, errEngineSource
	}
	return engineProfileNode{Device: uint64(stat.Dev), Inode: stat.Ino, Mode: stat.Mode, UID: stat.Uid, GID: stat.Gid, Size: stat.Size, Links: uint64(stat.Nlink)}, nil
}
func engineProfileNodeAt(path string) (*engineProfileNode, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errEngineSource
	}
	node, err := engineProfileStat(info)
	return &node, err
}
func engineProfileNamespaceAt(process, name string) (engineProfileNamespace, error) {
	info, err := os.Stat(filepath.Join(process, "ns", name))
	if err != nil {
		return engineProfileNamespace{}, errEngineSource
	}
	node, err := engineProfileStat(info)
	if err != nil || node.Device == 0 || node.Inode == 0 {
		return engineProfileNamespace{}, errEngineSource
	}
	return engineProfileNamespace{Device: node.Device, Inode: node.Inode}, nil
}

// RunEngineMountProfile reads only fixed namespace metadata, before native input.
func RunEngineMountProfile(ctx context.Context, output io.Writer) error {
	if ctx.Err() != nil || os.Getpid() <= 1 || os.Geteuid() != 1000 || os.Getegid() != 1000 {
		return errEngineSource
	}
	fields, err := engineProcessStatus("/proc/self")
	if err != nil || !engineProcessOwned(fields) || len(strings.Fields(fields["NSpid"])) != 1 {
		return errEngineSource
	}
	primary, err := engineProcessStatus("/proc/1")
	if err != nil || !engineProcessOwned(primary) || !slices.Equal(strings.Fields(primary["NSpid"]), []string{"1"}) {
		return errEngineSource
	}
	raw, err := engineProcessFile("/proc/1", "stat")
	pid, ticks, parseErr := parseEngineProcessStat(string(raw))
	if err != nil || parseErr != nil || pid != 1 {
		return errEngineSource
	}
	profile := engineMountProfile{Format: engineMountProfileFormat, PrimaryTicks: ticks}
	profile.UserNamespace, err = engineProfileNamespaceAt("/proc/1", "user")
	if err != nil {
		return err
	}
	profile.MountNamespace, err = engineProfileNamespaceAt("/proc/1", "mnt")
	if err != nil {
		return err
	}
	user, err := engineProfileNamespaceAt("/proc/self", "user")
	if err != nil || user != profile.UserNamespace {
		return errEngineSource
	}
	mount, err := engineProfileNamespaceAt("/proc/self", "mnt")
	if err != nil || mount != profile.MountNamespace {
		return errEngineSource
	}
	profile.Helper, err = engineProfileNodeAt("/proc/self/exe")
	if err != nil || profile.Helper == nil {
		return errEngineSource
	}
	helper, err := engineProfileNodeAt("/brine")
	if err != nil || helper == nil || *helper != *profile.Helper {
		return errEngineSource
	}
	for path, target := range map[string]**engineProfileNode{"/data": &profile.Data, "/keys/private.pem": &profile.PrivateKey, "/keys/public.pem": &profile.PublicKey, moduleDescriptorArtifactPath: &profile.Artifact} {
		*target, err = engineProfileNodeAt(path)
		if err != nil {
			return err
		}
	}
	raw, err = engineProcessFile("/proc/1", "stat")
	pid, current, parseErr := parseEngineProcessStat(string(raw))
	if err != nil || parseErr != nil || pid != 1 || current != ticks || ctx.Err() != nil {
		return errEngineSource
	}
	return json.NewEncoder(output).Encode(profile)
}

func decodeEngineMountProfile(raw []byte) (engineMountProfile, error) {
	var profile engineMountProfile
	if len(raw) == 0 || len(raw) > engineMountProfileLimit {
		return profile, errEngineSource
	}
	fields, err := strictjson.Object(raw, "format", "primary_ticks", "user_namespace", "mount_namespace", "helper", "data", "private_key", "public_key", "artifact")
	if err != nil {
		return profile, errEngineSource
	}
	for _, name := range []string{"user_namespace", "mount_namespace"} {
		if _, err = strictjson.Object(fields[name], "device", "inode"); err != nil {
			return profile, errEngineSource
		}
	}
	for _, name := range []string{"helper", "data", "private_key", "public_key", "artifact"} {
		if string(fields[name]) == "null" {
			continue
		}
		if _, err = strictjson.Object(fields[name], "device", "inode", "mode", "uid", "gid", "size", "links"); err != nil {
			return profile, errEngineSource
		}
	}
	if json.Unmarshal(raw, &profile) != nil || profile.Format != engineMountProfileFormat || profile.PrimaryTicks == 0 || profile.Helper == nil {
		return profile, errEngineSource
	}
	return profile, nil
}

func openEngineProfileHelper() (*os.File, error) {
	for parent := filepath.Dir(engineProfileHelper); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return nil, errEngineSource
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return nil, errEngineSource
		}
		if parent == "/" {
			break
		}
	}
	file, err := os.OpenFile(engineProfileHelper, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errEngineSource
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, errEngineSource
	}
	node, err := engineProfileStat(info)
	if err != nil || node.Mode != unix.S_IFREG|0755 || node.UID != 0 || node.GID != 0 || node.Links != 1 || node.Size <= 0 || node.Size > 64<<20 {
		_ = file.Close()
		return nil, errEngineSource
	}
	return file, nil
}
func openEngineProfileMember(path string, directory bool) (*os.File, error) {
	root, err := engineOpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	flags := uint64(unix.O_RDONLY)
	if directory {
		flags |= unix.O_DIRECTORY
	}
	file, err := engineOpenMember(root, filepath.Base(path), flags, 0)
	if err != nil {
		return nil, err
	}
	if _, err = engineMember(file, "member"); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
func openEngineProfileFiles(dataPath string, material engineMaterial, artifact *publicationArtifact) (files engineProfileFiles, err error) {
	defer func() {
		if err != nil {
			files.close()
		}
	}()
	files.helper, err = openEngineProfileHelper()
	if err != nil {
		return files, err
	}
	if dataPath != "" {
		files.data, err = openEngineProfileMember(dataPath, true)
		if err != nil {
			return files, err
		}
	}
	for path, target := range map[string]**os.File{material.privateKey: &files.privateKey, material.publicKey: &files.publicKey} {
		if path == "" {
			continue
		}
		*target, err = openEngineProfileMember(path, false)
		if err != nil {
			return files, err
		}
	}
	if artifact != nil {
		files.artifact, err = engineOpenMember(artifact.root, artifact.name, unix.O_RDONLY, 0)
		if err != nil {
			return files, err
		}
		info, e := files.artifact.Stat()
		if e != nil || !descriptorArtifactIdentity(info, artifact.member) {
			return files, errEngineSource
		}
	}
	return files, nil
}

func (f engineProfileFiles) snapshot() (engineMountProfile, error) {
	profile := engineMountProfile{}
	for _, member := range []struct {
		file   *os.File
		target **engineProfileNode
	}{{f.helper, &profile.Helper}, {f.data, &profile.Data}, {f.privateKey, &profile.PrivateKey}, {f.publicKey, &profile.PublicKey}, {f.artifact, &profile.Artifact}} {
		file, target := member.file, member.target
		if file == nil {
			continue
		}
		info, err := file.Stat()
		if err != nil {
			return profile, errEngineSource
		}
		node, err := engineProfileStat(info)
		if err != nil {
			return profile, errEngineSource
		}
		*target = &node
	}
	return profile, nil
}
func (f engineProfileFiles) helperUnchanged() bool {
	held, err := f.helper.Stat()
	current, currentErr := os.Stat(engineProfileHelper)
	return err == nil && currentErr == nil && os.SameFile(held, current)
}

func verifyEngineMountProfile(ctx context.Context, session localexec.Session, container engineContainer, handle *engineNativeHandle, files engineProfileFiles) error {
	return verifyEngineMountProfileCommand(ctx, session, container, handle, files, engineProfileStableCommand)
}

func verifyEngineMountProfileCommand(ctx context.Context, session localexec.Session, container engineContainer, handle *engineNativeHandle, files engineProfileFiles, mode engineProfileCommandMode) error {
	if !data.ValidCatalogHash(container.ID) || handle == nil || handle.alive() != nil || !files.helperUnchanged() {
		return errEngineSource
	}
	process := "/proc/" + strconv.FormatUint(uint64(handle.pid), 10)
	before, err := files.snapshot()
	if err != nil {
		return err
	}
	user, err := engineProfileNamespaceAt(process, "user")
	if err != nil {
		return err
	}
	mount, err := engineProfileNamespaceAt(process, "mnt")
	if err != nil {
		return err
	}
	mappings, err := readEngineProfileMappings(process)
	if err != nil {
		return err
	}
	identity := engineProfileIdentity{ticks: handle.ticks, user: user, mount: mount}
	if mode != engineProfileStartingCommand {
		identity.command, err = engineProcessFile(process, "cmdline")
		if err != nil {
			return err
		}
		identity.executable, err = os.Readlink(filepath.Join(process, "exe"))
		if err != nil {
			return errEngineSource
		}
	}
	result, err := session.Execute(ctx, "/usr/bin/podman", []string{"exec", "--user", "1000:1000", container.ID, "/brine", "host", "engine-mount-profile"}, nil, false)
	if err != nil || result.Truncated {
		return errEngineSource
	}
	observed, err := decodeEngineMountProfile([]byte(result.Stdout))
	if err != nil || !observed.boundTo(identity) || !mode.matches(before, observed, mappings) {
		return errEngineSource
	}
	after, err := files.snapshot()
	if err != nil || !engineProfileStable(before, after) || !files.helperUnchanged() || handle.alive() != nil || !engineNativeOwnership(process, handle.pid) {
		return errEngineSource
	}
	currentUser, err := engineProfileNamespaceAt(process, "user")
	if err != nil || currentUser != user {
		return errEngineSource
	}
	currentMount, err := engineProfileNamespaceAt(process, "mnt")
	if err != nil || currentMount != mount {
		return errEngineSource
	}
	currentMaps, err := readEngineProfileMappings(process)
	if err != nil || !mappings.equal(currentMaps) || ctx.Err() != nil {
		return errEngineSource
	}
	containers, err := listEngineContainers(ctx, session)
	if err != nil {
		return errEngineSource
	}
	for _, current := range containers {
		if current.ID != container.ID {
			continue
		}
		if !reflect.DeepEqual(current, container) {
			return errEngineSource
		}
		bound := engineProfileLifetimeBound(process, handle, identity)
		if mode != engineProfileStartingCommand {
			bound = engineProfileStillBound(process, handle, identity)
		}
		if !bound || !files.helperUnchanged() {
			return errEngineSource
		}
		final, err := files.snapshot()
		if err != nil || !engineProfileStable(before, final) || ctx.Err() != nil {
			return errEngineSource
		}
		return nil
	}
	return errEngineSource
}

func engineProfileStillBound(process string, handle *engineNativeHandle, identity engineProfileIdentity) bool {
	if !engineProfileLifetimeBound(process, handle, identity) {
		return false
	}
	command, err := engineProcessFile(process, "cmdline")
	if err != nil || !bytes.Equal(command, identity.command) {
		return false
	}
	executable, err := os.Readlink(filepath.Join(process, "exe"))
	return err == nil && executable == identity.executable
}

func engineProfileLifetimeBound(process string, handle *engineNativeHandle, identity engineProfileIdentity) bool {
	if handle.alive() != nil || !engineNativeOwnership(process, handle.pid) {
		return false
	}
	user, err := engineProfileNamespaceAt(process, "user")
	if err != nil || user != identity.user {
		return false
	}
	mount, err := engineProfileNamespaceAt(process, "mnt")
	if err != nil || mount != identity.mount {
		return false
	}
	return true
}
