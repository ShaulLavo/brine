//go:build !linux

package host

import (
	"context"
	"io"

	"github.com/ShaulLavo/brine/internal/datapublish"
)

func RunModuleDescriptorProfile(context.Context, io.Reader, io.Writer) error {
	return datapublish.ErrRecord
}
