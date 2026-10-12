//go:build !linux

package host

import (
	"context"
	"io"

	"github.com/ShaulLavo/brine/internal/datapublish"
)

func RunModuleStage(context.Context, io.Reader, io.Writer) error { return datapublish.ErrRecord }
