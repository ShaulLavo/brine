//go:build linux

package enroll

import (
	"context"
	"errors"
	"os"
	"runtime"
)

var nativeReaderDirectories = []string{"/opt/brine", "/opt/brine/native-reader", "/opt/brine/native-reader/brine-native-reader-v1"}

func (h *host) checkNativeReader(ctx context.Context) (bool, error) {
	pin, err := NativeReaderPin(runtime.GOARCH)
	if err != nil {
		return false, err
	}
	recorded, exists := h.r.Files[NativeReaderPinPath]
	if !exists {
		if _, err := os.Lstat(NativeReaderPinPath); !errors.Is(err, os.ErrNotExist) {
			return false, errors.New("unrecorded native reader pin refused")
		}
		return false, nil
	}
	if recorded.Mode != 0644 || recorded.Hash != sha256Hex(pin.Bytes()) || protectedParents(NativeReaderPinPath) != nil {
		return false, errors.New("protected native reader pin differs")
	}
	return h.fileMatches(NativeReaderPinPath, recorded.Hash)
}
func (h *host) installNativeReader(ctx context.Context) error {
	pin, err := NativeReaderPin(runtime.GOARCH)
	if err != nil {
		return err
	}
	for _, path := range nativeReaderDirectories {
		if err := h.prepareResticDirectory(path); err != nil {
			return err
		}
	}
	return h.file(ctx, NativeReaderPinPath, pin.Bytes(), 0644, false)
}
func (h *host) undoNativeReader(context.Context) error {
	if err := h.restoreFile(NativeReaderPinPath); err != nil {
		return err
	}
	for _, path := range []string{nativeReaderDirectories[2], nativeReaderDirectories[1]} {
		if err := h.removeResticDirectory(path); err != nil {
			return err
		}
	}
	return nil
}
