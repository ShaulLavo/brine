//go:build !linux

package inventory

import (
	"context"
	"fmt"
	"io/fs"
)

func (HostFS) ReadFile(context.Context, string) ([]byte, error) {
	return nil, fmt.Errorf("host filesystem inventory requires Linux")
}
func (HostFS) ReadDir(context.Context, string) ([]fs.DirEntry, error) {
	return nil, fmt.Errorf("host filesystem inventory requires Linux")
}
func (HostFS) Readlink(context.Context, string) (string, error) {
	return "", fmt.Errorf("host filesystem inventory requires Linux")
}
