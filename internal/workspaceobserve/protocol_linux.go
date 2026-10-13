//go:build linux

// Package workspaceobserve provides only scoped workspace-consumer observation.
package workspaceobserve

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ShaulLavo/brine/internal/strictjson"
	"golang.org/x/sys/unix"
)

const SocketPath = "/run/brine-workspace-observer/observe.sock"
const requestLimit = 1024
const observationTimeout = 5 * time.Second

var ErrUnknown = errors.New("workspace consumer observation unknown")
var ErrBusy = errors.New("workspace still has a consumer")

type directoryIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Mount  uint64 `json:"mount"`
}
type request struct {
	Nonce string `json:"nonce"`
}
type response struct {
	Nonce    string            `json:"nonce"`
	Identity directoryIdentity `json:"identity"`
	State    string            `json:"state"`
}

func identity(file *os.File) (directoryIdentity, uint32, error) {
	var stat unix.Statx_t
	if file == nil || unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &stat) != nil || stat.Mask&unix.STATX_MNT_ID == 0 || stat.Mnt_id == 0 || stat.Mode != unix.S_IFDIR|0700 {
		return directoryIdentity{}, 0, ErrUnknown
	}
	return directoryIdentity{unix.Mkdev(stat.Dev_major, stat.Dev_minor), stat.Ino, stat.Mnt_id}, stat.Uid, nil
}
func nonceValid(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 16 && hex.EncodeToString(raw) == value
}
func peer(connection *net.UnixConn) (*unix.Ucred, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return nil, ErrUnknown
	}
	var credentials *unix.Ucred
	if err = raw.Control(func(fd uintptr) { credentials, err = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil || credentials == nil {
		return nil, ErrUnknown
	}
	return credentials, err
}
func connectionDeadline(ctx context.Context, connection *net.UnixConn) func() bool {
	deadline := time.Now().Add(observationTimeout)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = connection.SetDeadline(deadline)
	return context.AfterFunc(ctx, func() { _ = connection.SetDeadline(time.Now()) })
}

// CheckAbsent returns no transferable proof. The caller retains its original
// guard and descriptor and must retire that same directory immediately.
func CheckAbsent(ctx context.Context, directory *os.File) error {
	return checkAbsent(ctx, SocketPath, directory, true)
}
func checkAbsent(ctx context.Context, socket string, directory *os.File, protect bool) error {
	held, uid, err := identity(directory)
	if err != nil || int64(uid) != int64(os.Geteuid()) || ctx.Err() != nil {
		return ErrUnknown
	}
	if protect && protectedSocket(socket) != nil {
		return ErrUnknown
	}
	dialer := net.Dialer{Timeout: observationTimeout}
	connected, err := dialer.DialContext(ctx, "unixpacket", socket)
	if err != nil {
		return ErrUnknown
	}
	connection, ok := connected.(*net.UnixConn)
	if !ok {
		_ = connected.Close()
		return ErrUnknown
	}
	defer func() { _ = connection.Close() }()
	stop := connectionDeadline(ctx, connection)
	defer stop()
	credentials, err := peer(connection)
	if err != nil || protect && credentials.Uid != 0 {
		return ErrUnknown
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return ErrUnknown
	}
	nonce := hex.EncodeToString(random[:])
	raw, _ := json.Marshal(request{Nonce: nonce})
	n, oob, err := connection.WriteMsgUnix(raw, unix.UnixRights(int(directory.Fd())), nil)
	if err != nil || n != len(raw) || oob != unix.CmsgSpace(4) {
		return ErrUnknown
	}
	buffer := make([]byte, requestLimit+1)
	ancillary := make([]byte, unix.CmsgSpace(4)*2)
	n, oob, flags, _, err := connection.ReadMsgUnix(buffer, ancillary)
	closeRights(ancillary[:oob])
	if err != nil || n > requestLimit || oob != 0 || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || ctx.Err() != nil {
		return ErrUnknown
	}
	fields, decodeErr := strictjson.Object(buffer[:n], "nonce", "identity", "state")
	if decodeErr != nil {
		return ErrUnknown
	}
	if _, err = strictjson.Object(fields["identity"], "device", "inode", "mount"); err != nil {
		return ErrUnknown
	}
	var observed response
	if json.Unmarshal(buffer[:n], &observed) != nil || observed.Nonce != nonce || observed.Identity != held {
		return ErrUnknown
	}
	current, currentUID, err := identity(directory)
	if err != nil || current != held || currentUID != uid {
		return ErrUnknown
	}
	switch observed.State {
	case "absent":
		return nil
	case "busy":
		return ErrBusy
	default:
		return ErrUnknown
	}
}
func closeRights(raw []byte) {
	messages, err := unix.ParseSocketControlMessage(raw)
	if err != nil {
		return
	}
	for _, message := range messages {
		if message.Header.Level != unix.SOL_SOCKET || message.Header.Type != unix.SCM_RIGHTS {
			continue
		}
		descriptors, err := unix.ParseUnixRights(&message)
		if err != nil {
			continue
		}
		for _, fd := range descriptors {
			_ = unix.Close(fd)
		}
	}
}
func protectedSocket(path string) error {
	if path != SocketPath {
		return ErrUnknown
	}
	for current := filepath.Dir(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return ErrUnknown
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return ErrUnknown
		}
		if current == "/" {
			break
		}
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode() != os.ModeSocket|0660 {
		return ErrUnknown
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Nlink != 1 {
		return ErrUnknown
	}
	return nil
}
func validWorkspaceRelative(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && parts[0] == "native-restores" && nonceValid(parts[1])
}
