//go:build !linux

package enroll

import (
	"context"
	"errors"
)

func AdmissionSettle(context.Context) error { return errors.New("admission settlement requires Linux") }
