package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/secrets"
	"golang.org/x/sys/unix"
)

// Secret input borrows the reader exclusively for this invocation. Files are
// polled without changing their flags or closing them. Other blocking readers
// must provide ReadContext; only bounded in-memory readers use plain Read.
func readSecretInput(ctx context.Context, input io.Reader) ([]byte, error) {
	var read func([]byte) (int, error)
	switch r := input.(type) {
	case *os.File:
		raw, err := r.SyscallConn()
		if err != nil {
			return nil, result.New(result.InvalidUsage, err)
		}
		var fd int
		if err := raw.Control(func(descriptor uintptr) { fd = int(descriptor) }); err != nil {
			return nil, result.New(result.InvalidUsage, err)
		}
		read = func(p []byte) (int, error) {
			for {
				if err := ctx.Err(); err != nil {
					return 0, err
				}
				fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
				n, err := unix.Poll(fds, 50)
				if errors.Is(err, unix.EINTR) {
					continue
				}
				if err != nil {
					return 0, err
				}
				if n == 0 {
					continue
				}
				if err := ctx.Err(); err != nil {
					return 0, err
				}
				if fds[0].Revents&unix.POLLNVAL != 0 {
					return 0, os.ErrInvalid
				}
				return r.Read(p)
			}
		}
	case interface {
		ReadContext(context.Context, []byte) (int, error)
	}:
		read = func(p []byte) (int, error) { return r.ReadContext(ctx, p) }
	case *strings.Reader, *bytes.Reader, *bytes.Buffer:
		read = r.Read
	default:
		return nil, result.New(result.InvalidUsage, nil)
	}
	inputBuffer := make([]byte, secrets.ValueLimit+1)
	n := 0
	for n < len(inputBuffer) {
		if err := ctx.Err(); err != nil {
			clear(inputBuffer)
			return nil, err
		}
		count, err := read(inputBuffer[n:])
		n += count
		if canceled := ctx.Err(); canceled != nil {
			clear(inputBuffer)
			return nil, canceled
		}
		if err == io.EOF {
			return inputBuffer[:n], nil
		}
		if err != nil || count == 0 {
			clear(inputBuffer)
			return nil, result.New(result.InvalidUsage, err)
		}
	}
	return inputBuffer, nil
}
