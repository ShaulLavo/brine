package quadlet

import "errors"

var (
	ErrPublicationUnknown = errors.New("quadlet: publication requires reconciliation")
	ErrUnowned            = errors.New("quadlet: artifact is not owned by Brine")
	ErrDrift              = errors.New("quadlet: recorded artifact has drifted")
)
