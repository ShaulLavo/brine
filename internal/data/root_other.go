//go:build !linux

package data

import "context"

func InspectRoot(string) (RootEvidence, error)                { return RootEvidence{}, ErrInvalid }
func ProbeRoot(context.Context, string) (RootEvidence, error) { return RootEvidence{}, ErrInvalid }
