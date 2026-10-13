//go:build !linux

package host

import (
	"context"
	"io"
)

func RunEngineMountProfile(context.Context, io.Writer) error { return errEngineSource }
