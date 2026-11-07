//go:build !linux

package store

import "context"

func CaptureBytes(context.Context, string, int) (Capture, error)    { return Capture{}, errHostOnly }
func previewReadOwned(context.Context, string, int) ([]byte, error) { return nil, errHostOnly }
