//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"golang.org/x/sys/unix"
	"os"
	"strings"
	"testing"
)

func engineProfileMappingFixture(t *testing.T) engineProfileMappings {
	t.Helper()
	raw := []byte("0 165536 1000\n1000 1001 1\n1001 166536 64536\n")
	m, err := parseEngineIDMap(raw, 65534)
	if err != nil {
		t.Fatal(err)
	}
	return engineProfileMappings{uid: m, gid: m}
}
func engineProfileFixture(t *testing.T) (engineMountProfile, engineMountProfile, engineProfileMappings) {
	t.Helper()
	m := engineProfileMappingFixture(t)
	host := engineMountProfile{Helper: &engineProfileNode{Device: 45826, Inode: 49, Mode: unix.S_IFREG | 0755, UID: 0, GID: 0, Size: 14000000, Links: 1}, Data: &engineProfileNode{Device: 45826, Inode: 270191, Mode: unix.S_IFDIR | 0700, UID: 1001, GID: 1001, Size: 4096, Links: 2}, PrivateKey: &engineProfileNode{Device: 45826, Inode: 270194, Mode: unix.S_IFREG | 0600, UID: 1001, GID: 1001, Size: 240, Links: 1}, PublicKey: &engineProfileNode{Device: 45826, Inode: 270195, Mode: unix.S_IFREG | 0600, UID: 1001, GID: 1001, Size: 128, Links: 1}}
	raw, _ := json.Marshal(host)
	var guest engineMountProfile
	_ = json.Unmarshal(raw, &guest)
	guest.Format = engineMountProfileFormat
	guest.PrimaryTicks = 42
	guest.UserNamespace = engineProfileNamespace{Device: 4, Inode: 100}
	guest.MountNamespace = engineProfileNamespace{Device: 4, Inode: 101}
	for _, node := range []*engineProfileNode{guest.Helper, guest.Data, guest.PrivateKey, guest.PublicKey} {
		node.UID = m.uid.namespaceID(node.UID)
		node.GID = m.gid.namespaceID(node.GID)
	}
	return host, guest, m
}
func TestEngineProfileMapsActualNamespaceViewAndRejectsForeignMembers(t *testing.T) {
	host, guest, m := engineProfileFixture(t)
	if !engineProfileMatches(host, guest, m) {
		t.Fatal("actual keep-id view refused")
	}
	if guest.Helper.UID != 65534 || guest.Data.UID != 1000 {
		t.Fatal("root overflow/runner mappings changed")
	}
	for _, field := range []string{"device", "inode", "mode", "uid", "gid", "size", "links", "missing", "extra"} {
		t.Run(field, func(t *testing.T) { testEngineProfileMemberAttack(t, host, guest, m, field) })
	}
	mapped, err := parseEngineIDMap([]byte("77 0 1\n1000 1001 1\n"), 65534)
	if err != nil {
		t.Fatal(err)
	}
	if engineProfileMatches(host, guest, engineProfileMappings{uid: mapped, gid: mapped}) {
		t.Fatal("overflow accepted for mapped root")
	}
	guest.Data.Size = 8192
	guest.Data.Links = 4
	if !engineProfileMatches(host, guest, m) {
		t.Fatal("ordinary live directory writes refused")
	}
}

func TestEngineRunningProfileAllowsAtomicallyReplacedHelper(t *testing.T) {
	host, guest, mappings := engineProfileFixture(t)
	guest.Helper.Inode++
	guest.Helper.Size -= 4096
	guest.Helper.Links = 0
	t.Logf("installed helper inode=%d size=%d links=%d; running helper inode=%d size=%d links=%d", host.Helper.Inode, host.Helper.Size, host.Helper.Links, guest.Helper.Inode, guest.Helper.Size, guest.Helper.Links)
	if !engineProfileRunningCommand.matches(host, guest, mappings) {
		t.Fatal("running engine ownership rejected after atomic helper replacement")
	}
	if engineProfileStableCommand.matches(host, guest, mappings) || engineProfileStartingCommand.matches(host, guest, mappings) {
		t.Fatal("new startup or publication accepted the replaced helper")
	}
	guest.PrivateKey.Inode++
	if engineProfileRunningCommand.matches(host, guest, mappings) {
		t.Fatal("foreign key admitted with a replaced helper")
	}
}

func TestEngineRunningProfileRefusesForeignHelper(t *testing.T) {
	for _, field := range []string{"linked", "owner", "writable", "device", "empty"} {
		t.Run(field, func(t *testing.T) { testEngineRunningHelperAttack(t, field) })
	}
}

func testEngineRunningHelperAttack(t *testing.T, field string) {
	t.Helper()
	host, guest, mappings := engineProfileFixture(t)
	guest.Helper.Inode++
	guest.Helper.Links = 0
	switch field {
	case "linked":
		guest.Helper.Links = 1
	case "owner":
		guest.Helper.UID = 1000
	case "writable":
		guest.Helper.Mode = unix.S_IFREG | 0775
	case "device":
		guest.Helper.Device++
	case "empty":
		guest.Helper.Size = 0
	}
	if engineProfileRunningCommand.matches(host, guest, mappings) {
		t.Fatal("foreign helper admitted", field)
	}
}
func testEngineProfileMemberAttack(t *testing.T, host, guest engineMountProfile, m engineProfileMappings, field string) {
	t.Helper()
	node := *guest.PrivateKey
	guest.PrivateKey = &node
	switch field {
	case "device":
		node.Device++
	case "inode":
		node.Inode++
	case "mode":
		node.Mode = unix.S_IFREG | 0644
	case "uid":
		node.UID = 65534
	case "gid":
		node.GID++
	case "size":
		node.Size++
	case "links":
		node.Links++
	case "missing":
		guest.PrivateKey = nil
	case "extra":
		guest.Artifact = &node
	}
	if engineProfileMatches(host, guest, m) {
		t.Fatal("foreign namespace member admitted", field)
	}
}
func TestEngineProfileStrictFrameRejectsStructuralClaims(t *testing.T) {
	_, guest, _ := engineProfileFixture(t)
	raw, err := json.Marshal(guest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeEngineMountProfile(raw); err != nil {
		t.Fatal(err)
	}
	for _, attack := range []string{
		string(raw) + string(raw), strings.Replace(string(raw), `"primary_ticks":42`, `"primary_ticks":0`, 1), strings.Replace(string(raw), `"device":4`, `"Device":4`, 1), strings.Replace(string(raw), `"format":`, `"unknown":true,"format":`, 1), strings.Replace(string(raw), `"primary_ticks":42`, `"primary_ticks":42,"primary_ticks":42`, 1), strings.Replace(string(raw), engineMountProfileFormat, "caller", 1), `null`, strings.Repeat("x", engineMountProfileLimit+1),
	} {
		if _, err = decodeEngineMountProfile([]byte(attack)); err == nil {
			t.Fatal("forged frame admitted")
		}
	}
}
func TestEngineProfileHeldDescriptorRefusesRegularDrift(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "held")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err = file.Write([]byte("immutable")); err != nil {
		t.Fatal(err)
	}
	files := engineProfileFiles{helper: file}
	before, err := files.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte(" changed")); err != nil {
		t.Fatal(err)
	}
	after, err := files.snapshot()
	if err != nil || engineProfileStable(before, after) {
		t.Fatal("held regular descriptor drift admitted", err)
	}
}
func TestEngineProfileNoInputHelperRejectsOutsideNamespace(t *testing.T) {
	var output bytes.Buffer
	if RunEngineMountProfile(context.Background(), &output) == nil || output.Len() != 0 {
		t.Fatal("host process emitted contained authority")
	}
}

func TestEngineProfileBindsExactPrimaryLifetimeAndNamespaces(t *testing.T) {
	_, guest, _ := engineProfileFixture(t)
	expected := engineProfileIdentity{ticks: guest.PrimaryTicks, user: guest.UserNamespace, mount: guest.MountNamespace}
	if !guest.boundTo(expected) {
		t.Fatal("exact primary identity refused")
	}
	for _, attack := range []string{"ticks", "user", "mount"} {
		current := guest
		switch attack {
		case "ticks":
			current.PrimaryTicks++
		case "user":
			current.UserNamespace.Inode++
		case "mount":
			current.MountNamespace.Inode++
		}
		if current.boundTo(expected) {
			t.Fatal("different primary lifetime or namespace admitted", attack)
		}
	}
}
