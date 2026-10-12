//go:build linux

package workspaceobserve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func fixture(t *testing.T) (scope, *os.File, string, directoryIdentity) {
	t.Helper()
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil { //nolint:gosec // Owned persistent-root fixture requires traversal.
		t.Fatal(err)
	} //nolint:gosec // Authorized persistent root fixture.
	path := filepath.Join(base, "native-restores", strings.Repeat("a", 32))
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := openDirectory(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	held, uid, err := identity(file)
	if err != nil {
		t.Fatal(err)
	}
	return scope{uid: uid, roots: []string{base}}, file, path, held
}
func currentPID(t *testing.T) int32 {
	t.Helper()
	value := int64(os.Getpid())
	if value <= 0 || value > math.MaxInt32 {
		t.Fatal("fixture PID invalid")
		return 0
	}
	return int32(value)
}
func procFixture(t *testing.T) *os.Root {
	t.Helper()
	root, err := os.OpenRoot("/proc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}
func TestObserverHeldRootIsOnlyExactCallerSelfReference(t *testing.T) {
	s, _, path, held := fixture(t)
	root := procFixture(t)
	pid := strconv.Itoa(os.Getpid())
	if state := s.scanProcess(context.Background(), root, pid, path, held, currentPID(t)); state != "absent" {
		t.Fatal("held root self-reference refused", state)
	}
	marker := filepath.Join(path, "marker")
	file, err := os.OpenFile(marker, os.O_CREATE|os.O_RDWR, 0600) // #nosec G304 -- Fixed marker under the test-owned workspace.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if state := s.scanProcess(context.Background(), root, pid, path, held, currentPID(t)); state != "busy" {
		t.Fatal("own file consumer exempted", state)
	}
	if err = os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if state := s.scanProcess(context.Background(), root, pid, path, held, currentPID(t)); state != "busy" {
		t.Fatal("deleted file consumer missed", state)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	task := pid + "/task/" + strconv.Itoa(unix.Gettid())
	start, startErr := processStart(root, task)
	members, memberErr := workspaceMembers(context.Background(), mustDirectory(t, path), s.uid, held)
	if startErr != nil || memberErr != nil {
		t.Fatal("closed consumer census", startErr, memberErr)
	}
	if state := s.scanTask(context.Background(), root, task, start, path, held, members, currentPID(t)); state != "absent" {
		t.Fatal("closed consumer remains busy", state)
	}
	if state := s.scanProcess(context.Background(), root, pid, path, held, currentPID(t)); state == "busy" {
		t.Fatal("closed consumer remains in process view", state)
	}
}
func TestObserverOutsideAliasIdentityRemainsBusyAfterAliasUnlink(t *testing.T) {
	s, _, path, held := fixture(t)
	marker := filepath.Join(path, "marker")
	outside := filepath.Join(t.TempDir(), "alias")
	if err := os.WriteFile(marker, []byte("marker"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(marker, outside); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(outside) // #nosec G304 -- Fixed outside alias under this test-owned temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err = os.Remove(outside); err != nil {
		t.Fatal(err)
	}
	root := procFixture(t)
	members, memberErr := workspaceMembers(context.Background(), mustDirectory(t, path), s.uid, held)
	if memberErr != nil {
		t.Fatal("alias member census", memberErr)
	}
	busy, fdErr := consumerFD(root, strconv.Itoa(os.Getpid())+"/task/"+strconv.Itoa(unix.Gettid()), strconv.FormatUint(uint64(file.Fd()), 10), path, held, members, currentPID(t))
	if fdErr != nil || !busy {
		t.Fatal("direct alias descriptor check", busy, fdErr)
	}
	if state := s.scanProcess(context.Background(), root, strconv.Itoa(os.Getpid()), path, held, currentPID(t)); state != "busy" {
		t.Fatal("outside same-inode alias missed", state)
	}
}
func TestObserverDirectoryAuthorityRefusesAliasForeignModeAndStaleIdentity(t *testing.T) {
	s, file, path, held := fixture(t)
	if _, _, err := s.authorize(file); err != nil {
		t.Fatal("actual owned descriptor refused", err)
	}
	foreign := s
	foreign.uid++
	if _, _, err := foreign.authorize(file); err == nil {
		t.Fatal("foreign owner admitted")
	}
	denied := s
	denied.roots = nil
	if _, _, err := denied.authorize(file); err == nil {
		t.Fatal("out-of-root descriptor admitted")
	}
	if err := os.Chmod(path, 0755); err != nil { //nolint:gosec // Deliberately nonprivate directory refusal.
		t.Fatal(err)
	} //nolint:gosec // Unsafe private-directory fixture.
	if _, _, err := s.authorize(file); err == nil {
		t.Fatal("nonprivate descriptor admitted")
	}
	if err := os.Chmod(path, 0700); err != nil { //nolint:gosec // Restore private directory traversal.
		t.Fatal(err)
	} //nolint:gosec // Restore fixture traversal.
	changed := held
	changed.Mount++
	if s.authorizedAlias(s.roots[0], "native-restores/"+filepath.Base(path), changed) == nil {
		t.Fatal("mount identity ignored")
	}
	if err := os.Rename(path, path+"-retained"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.authorize(file); err == nil {
		t.Fatal("stale/renamed descriptor admitted")
	}
}
func TestObserverMetadataCensusDistinguishesEmptyUnsafeAndCancelled(t *testing.T) {
	s, file, path, held := fixture(t)
	members, err := workspaceMembers(context.Background(), file, s.uid, held)
	if err != nil || len(members) != 1 {
		t.Fatal("complete empty directory refused", err)
	}
	marker := filepath.Join(path, "marker")
	if err = os.WriteFile(marker, []byte("marker"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(marker, filepath.Join(t.TempDir(), "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err = workspaceMembers(context.Background(), file, s.uid, held); err == nil {
		t.Fatal("outside hardlink alias admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = workspaceMembers(ctx, file, s.uid, held); err == nil {
		t.Fatal("cancelled census admitted")
	}
}
func TestObserverUnixPacketBindsActualDescriptorAndNonce(t *testing.T) {
	_, directory, _, held := fixture(t)
	scratch, err := os.MkdirTemp("", "obs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	socket := filepath.Join(scratch, "socket")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: socket, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if err = enableCredentials(listener); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		connection, err := listener.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = connection.Close() }()
		buffer, control := make([]byte, 1024), make([]byte, 256)
		n, oob, _, _, err := connection.ReadMsgUnix(buffer, control)
		if err != nil {
			done <- err
			return
		}
		credentials, err := peer(connection)
		if err != nil {
			done <- err
			return
		}
		file, err := requestDescriptor(control[:oob], credentials)
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = file.Close() }()
		observed, _, err := identity(file)
		if err != nil || observed != held {
			done <- fmt.Errorf("descriptor identity changed")
			return
		}
		var input request
		if json.Unmarshal(buffer[:n], &input) != nil || !nonceValid(input.Nonce) {
			done <- ErrUnknown
			return
		}
		raw, _ := json.Marshal(response{Nonce: input.Nonce, Identity: observed, State: "absent"})
		_, _, err = connection.WriteMsgUnix(raw, nil, nil)
		done <- err
	}()
	if err = checkAbsent(context.Background(), socket, directory, false); err != nil {
		t.Fatal("actual descriptor protocol", err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = CheckAbsent(context.Background(), directory); !errors.Is(err, ErrUnknown) {
		t.Fatal("missing protected endpoint admitted", err)
	}
}

// The physical lane runs this compiled helper as the enrolled UID against the
// reviewed root service. Its output is only the closed consumer state.
func TestObserverPhysicalProbe(t *testing.T) {
	path := os.Getenv("BRINE_OBSERVER_TEST_DIRECTORY")
	if path == "" {
		t.Skip("physical scoped observer probe")
	}
	directory, err := openDirectory(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	err = CheckAbsent(context.Background(), directory)
	state := "unknown"
	if err == nil {
		state = "absent"
	} else if errors.Is(err, ErrBusy) {
		state = "busy"
	}
	if _, printErr := fmt.Fprintln(os.Stdout, "BRINE_OBSERVER_STATE="+state); printErr != nil {
		t.Fatal(printErr)
	}
	if wanted := os.Getenv("BRINE_OBSERVER_TEST_STATE"); wanted != "" && wanted != state {
		t.Fatalf("consumer state=%s, wanted %s", state, wanted)
	}
}

func TestObserverDescriptorBoundaryRefusesExtraForeignAndMissingRights(t *testing.T) {
	s, directory, _, _ := fixture(t)
	sender := &unix.Ucred{Uid: s.uid, Gid: 0, Pid: currentPID(t)}
	for _, kind := range []string{"exact", "extra", "foreign", "missing"} {
		duplicate, err := unix.Dup(int(directory.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		rights := unix.UnixRights(duplicate)
		credentials := *sender
		if kind == "extra" {
			extra, err := unix.Dup(int(directory.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			rights = unix.UnixRights(duplicate, extra)
		}
		if kind == "foreign" {
			credentials.Uid++
		}
		if kind == "missing" {
			_ = unix.Close(duplicate)
			rights = nil
		}
		control := append([]byte(nil), rights...)
		control = append(control, unix.UnixCredentials(&credentials)...)
		received, err := requestDescriptor(control, sender)
		if kind == "exact" {
			if err != nil {
				t.Fatal("actual right refused", err)
			}
			_ = received.Close()
			continue
		}
		if err == nil {
			_ = received.Close()
			t.Fatal("unqualified descriptor accepted", kind)
		}
	}
}

func mustDirectory(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := openDirectory(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestObserverGrantAndReplyBoundariesRemainClosed(t *testing.T) {
	for _, raw := range []string{"", DefaultGrant, "allow_workspace_observation = false\n"} {
		allow, err := ParseGrant([]byte(raw))
		if err != nil || allow {
			t.Fatal("default grant not denied", err)
		}
	}
	if allow, err := ParseGrant([]byte("allow_workspace_observation = true\n")); err != nil || !allow {
		t.Fatal("explicit grant refused", err)
	}
	for _, raw := range []string{"allow_workspace_observation = \"true\"\n", "allow_workspace_observation = true\npath = \"/private\"\n"} {
		if _, err := ParseGrant([]byte(raw)); err == nil {
			t.Fatal("malformed grant accepted")
		}
	}
	s, directory, _, _ := fixture(t)
	credential := &unix.Ucred{Uid: s.uid, Pid: currentPID(t)}
	denied := observeRequest(context.Background(), strings.Repeat("1", 32), directory, credential, func() (scope, error) { return scope{}, ErrUnknown })
	if denied.State != "unknown" || denied.Identity != (directoryIdentity{}) {
		t.Fatal("denied scope observed data")
	}
	credential.Uid++
	foreign := observeRequest(context.Background(), strings.Repeat("1", 32), directory, credential, func() (scope, error) { return s, nil })
	if foreign.State != "unknown" || foreign.Identity != (directoryIdentity{}) {
		t.Fatal("foreign peer observed data")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkAbsent(ctx, "/unavailable", directory, false); !errors.Is(err, ErrUnknown) {
		t.Fatal("cancelled client admitted absence", err)
	}
	regular, err := os.OpenFile(filepath.Join(t.TempDir(), "file"), os.O_CREATE|os.O_RDWR, 0600) // #nosec G304 -- Fixed file under this test-owned temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = regular.Close() }()
	if _, _, err = identity(regular); err == nil {
		t.Fatal("non-directory FD admitted")
	}
}
