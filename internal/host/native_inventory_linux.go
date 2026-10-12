//go:build linux

package host

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/klauspost/compress/zstd"
	"golang.org/x/sys/unix"
)

func nativeOwnedInventory(ctx context.Context, root *os.Root, seal engineTreeSeal, binding, epoch, capture string) (nativecapture.Inventory, error) {
	replica, err := nativeSoleReplica(seal)
	if err != nil {
		return nativecapture.Inventory{}, err
	}
	scope := nativecapture.Scope{BindingID: binding, EpochID: epoch, CaptureID: capture, ReplicaID: replica}
	prefix := "replicas/" + strconv.FormatUint(replica, 10) + "/"
	var members []nativecapture.Member
	for _, member := range seal.members {
		if member.Mode&unix.S_IFMT != unix.S_IFREG || !strings.HasPrefix(member.Name, prefix) || strings.HasSuffix(member.Name, "db.lock") {
			continue
		}
		if !strings.Contains(member.Name, "/clog/") && !strings.Contains(member.Name, "/snapshots/") {
			continue
		}
		if member.Size <= 0 || member.Size > 32<<20 {
			return nativecapture.Inventory{}, errEngineSource
		}
		expanded, err := nativeMemberExpandedBytes(ctx, root, member)
		if err != nil {
			return nativecapture.Inventory{}, err
		}
		members = append(members, nativecapture.Member{Path: member.Name, Bytes: uint64(member.Size), ExpandedBytes: expanded, SHA256: member.SHA256, Device: member.Device, Inode: member.Inode, MountID: member.MountID, UID: member.UID, Mode: 0600, Links: uint64(member.Links)})
	}
	return nativecapture.NewInventory(scope, members, nativecapture.DefaultLimits())
}

func nativeSoleReplica(seal engineTreeSeal) (uint64, error) {
	var replica uint64
	found := false
	for _, member := range seal.members {
		if !strings.HasPrefix(member.Name, "replicas/") {
			continue
		}
		parts := strings.Split(member.Name, "/")
		if len(parts) < 2 {
			return 0, errEngineSource
		}
		value, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil || strconv.FormatUint(value, 10) != parts[1] || found && replica != value {
			return 0, errEngineSource
		}
		replica, found = value, true
	}
	if !found {
		return 0, errEngineSource
	}
	return replica, nil
}

func nativeMemberExpandedBytes(ctx context.Context, root *os.Root, member engineTreeMember) (uint64, error) {
	if member.Size <= 0 || member.Size > 32<<20 {
		return 0, errEngineSource
	}
	file, err := engineOpenMember(root, member.Name, unix.O_RDONLY, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	actual, err := engineMember(file, member.Name)
	if err != nil || actual.Size != member.Size {
		return 0, errEngineSource
	}
	var magic [4]byte
	n, err := file.ReadAt(magic[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, errEngineSource
	}
	if n < 4 || binary.LittleEndian.Uint32(magic[:]) != 0xfd2fb528 {
		return uint64(member.Size), nil
	}
	decoder, err := zstd.NewReader(&engineBoundedReader{ctx: ctx, reader: file}, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20), zstd.WithDecoderMaxWindow(8<<20), zstd.WithDecoderLowmem(true))
	if err != nil {
		return 0, errEngineSource
	}
	defer decoder.Close()
	expanded, err := io.CopyBuffer(io.Discard, io.LimitReader(decoder, (64<<20)+1), make([]byte, 16<<10))
	if err != nil || expanded <= 0 || expanded > 64<<20 || ctx.Err() != nil {
		return 0, errEngineSource
	}
	fresh, err := engineMember(file, member.Name)
	if err != nil || fresh != actual {
		return 0, errEngineSource
	}
	return uint64(expanded), nil
}
