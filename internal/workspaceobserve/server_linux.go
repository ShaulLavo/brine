//go:build linux

package workspaceobserve

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/ShaulLavo/brine/internal/strictjson"
	"golang.org/x/sys/unix"
)

// Serve has no command, path, PID, or policy argument. Only the operator starts
// this fixed private service; public dispatch cannot start or configure it.
func Serve(ctx context.Context) error {
	if os.Geteuid() != 0 {
		return ErrUnknown
	}
	enrolled, err := enrolled(false)
	if err != nil || int64(os.Getegid()) != int64(enrolled.gid) {
		return ErrUnknown
	}
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: SocketPath, Net: "unixpacket"})
	if err != nil {
		return ErrUnknown
	}
	listener.SetUnlinkOnClose(false)
	if enableCredentials(listener) != nil {
		_ = listener.Close()
		return ErrUnknown
	}
	defer func() { _ = listener.Close() }()
	if os.Chmod(SocketPath, 0660) != nil { //nolint:gosec // Protected root:enrolled-group socket additionally authenticates the kernel UID.
		return ErrUnknown
	}
	for ctx.Err() == nil {
		_ = listener.SetDeadline(time.Now().Add(time.Second))
		connection, err := listener.AcceptUnix()
		if err != nil {
			continue
		}
		serveConnection(ctx, connection, loadScope)
		_ = connection.Close()
	}
	return ctx.Err()
}
func serveConnection(parent context.Context, connection *net.UnixConn, load func() (scope, error)) {
	ctx, cancel := context.WithTimeout(parent, observationTimeout)
	defer cancel()
	stop := connectionDeadline(ctx, connection)
	defer stop()
	credentials, err := peer(connection)
	if err != nil {
		return
	}
	if enableCredentials(connection) != nil {
		return
	}
	buffer := make([]byte, requestLimit+1)
	ancillary := make([]byte, unix.CmsgSpace(4*16)+unix.CmsgSpace(unix.SizeofUcred))
	n, oob, flags, _, err := connection.ReadMsgUnix(buffer, ancillary)
	if err != nil || n > requestLimit || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		closeRights(ancillary[:oob])
		return
	}
	descriptor, err := requestDescriptor(ancillary[:oob], credentials)
	if err != nil {
		return
	}
	defer func() { _ = descriptor.Close() }()
	if _, err = strictjson.Object(buffer[:n], "nonce"); err != nil {
		return
	}
	var input request
	if json.Unmarshal(buffer[:n], &input) != nil || !nonceValid(input.Nonce) {
		return
	}
	observed := observeRequest(ctx, input.Nonce, descriptor, credentials, load)
	encoded, _ := json.Marshal(observed)
	_, _, _ = connection.WriteMsgUnix(encoded, nil, nil)
}
func requestDescriptor(control []byte, peer *unix.Ucred) (*os.File, error) {
	messages, err := unix.ParseSocketControlMessage(control)
	if err != nil {
		closeRights(control)
		return nil, ErrUnknown
	}
	var descriptors []int
	var sender *unix.Ucred
	valid := len(messages) == 2
	for _, message := range messages {
		if message.Header.Level != unix.SOL_SOCKET {
			valid = false
			continue
		}
		switch message.Header.Type {
		case unix.SCM_RIGHTS:
			rights, err := unix.ParseUnixRights(&message)
			if err != nil {
				valid = false
				continue
			}
			descriptors = append(descriptors, rights...)
		case unix.SCM_CREDENTIALS:
			credentials, err := unix.ParseUnixCredentials(&message)
			if err != nil || sender != nil {
				valid = false
				continue
			}
			sender = credentials
		default:
			valid = false
		}
	}
	for _, fd := range descriptors {
		unix.CloseOnExec(fd)
	}
	if !valid || len(descriptors) != 1 || sender == nil || sender.Uid != peer.Uid || sender.Gid != peer.Gid || sender.Pid != peer.Pid {
		for _, fd := range descriptors {
			_ = unix.Close(fd)
		}
		return nil, ErrUnknown
	}
	return os.NewFile(uintptr(descriptors[0]), "workspace"), nil
}
func observeRequest(ctx context.Context, nonce string, directory *os.File, peer *unix.Ucred, load func() (scope, error)) response {
	result := response{Nonce: nonce, State: "unknown"}
	current, err := load()
	if err != nil || peer.Uid != current.uid || peer.Pid <= 0 {
		return result
	}
	held, path, err := current.authorize(directory)
	if err != nil {
		return result
	}
	result.Identity = held
	result.State = current.scan(ctx, path, held, peer.Pid)
	refreshed, err := load()
	if err != nil || refreshed.uid != current.uid || refreshed.gid != current.gid || refreshed.policyHash != current.policyHash || ctx.Err() != nil {
		result.State = "unknown"
		return result
	}
	after, afterPath, err := refreshed.authorize(directory)
	if err != nil || after != held || afterPath != path {
		result.State = "unknown"
	}
	return result
}

func enableCredentials(socket interface {
	SyscallConn() (syscall.RawConn, error)
}) error {
	raw, err := socket.SyscallConn()
	if err != nil {
		return ErrUnknown
	}
	var optionErr error
	if raw.Control(func(fd uintptr) { optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1) }) != nil || optionErr != nil {
		return ErrUnknown
	}
	return nil
}
