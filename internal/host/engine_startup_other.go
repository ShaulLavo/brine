//go:build !linux

package host

import (
	"context"
	"io"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
)

type EngineStartupRuntime struct{}

func OpenEngineStartup(context.Context) (*EngineStartupRuntime, error)  { return nil, data.ErrInvalid }
func (*EngineStartupRuntime) Close() error                              { return nil }
func (*EngineStartupRuntime) EngineExec(context.Context, string) error  { return data.ErrInvalid }
func RunEngineCopyObserver(context.Context, io.Reader, io.Writer) error { return data.ErrInvalid }

func WriteEngineMaterial(context.Context, *store.EnginePreparation, data.EngineRuntime, *EngineSecretMaterial) error {
	return data.ErrInvalid
}
