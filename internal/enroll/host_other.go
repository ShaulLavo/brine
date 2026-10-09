//go:build !linux

package enroll

import (
	"context"
	"errors"
)

func HostOperation(context.Context, HostRequest) (any, error) {
	return nil, errors.New("enrollment host operations require Linux")
}
