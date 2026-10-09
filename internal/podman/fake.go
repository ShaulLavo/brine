package podman

import (
	"context"

	"github.com/ShaulLavo/brine/internal/localexec"
)

// Fake fails closed when an operation has not been configured. Callbacks own
// their state and synchronization, so concurrent tests need no shared recorder.
type Fake struct {
	VersionFunc        func(context.Context) (Version, error)
	PullFunc           func(context.Context, Image) error
	InspectFunc        func(context.Context, Image) (ImageInfo, error)
	ImageExistsFunc    func(context.Context, Image) (bool, error)
	CreateSecretFunc   func(context.Context, Name, []byte) error
	SecretExistsFunc   func(context.Context, Name) (bool, error)
	SecretNamesFunc    func(context.Context) ([]Name, error)
	ContainerStateFunc func(context.Context, Name) (ContainerState, error)
}

func unconfigured() error { return &localexec.Error{Kind: localexec.Failed} }
func (f *Fake) Version(c context.Context) (Version, error) {
	if f.VersionFunc != nil {
		return f.VersionFunc(c)
	}
	return Version{}, unconfigured()
}
func (f *Fake) Pull(c context.Context, i Image) error {
	if f.PullFunc != nil {
		return f.PullFunc(c, i)
	}
	return unconfigured()
}
func (f *Fake) Inspect(c context.Context, i Image) (ImageInfo, error) {
	if f.InspectFunc != nil {
		return f.InspectFunc(c, i)
	}
	return ImageInfo{}, unconfigured()
}
func (f *Fake) ImageExists(c context.Context, i Image) (bool, error) {
	if f.ImageExistsFunc != nil {
		return f.ImageExistsFunc(c, i)
	}
	return false, unconfigured()
}
func (f *Fake) CreateSecret(c context.Context, n Name, r []byte) error {
	if f.CreateSecretFunc != nil {
		return f.CreateSecretFunc(c, n, r)
	}
	return unconfigured()
}
func (f *Fake) SecretExists(c context.Context, n Name) (bool, error) {
	if f.SecretExistsFunc != nil {
		return f.SecretExistsFunc(c, n)
	}
	return false, unconfigured()
}
func (f *Fake) SecretNames(c context.Context) ([]Name, error) {
	if f.SecretNamesFunc != nil {
		return f.SecretNamesFunc(c)
	}
	return nil, unconfigured()
}
func (f *Fake) ContainerState(c context.Context, n Name) (ContainerState, error) {
	if f.ContainerStateFunc != nil {
		return f.ContainerStateFunc(c, n)
	}
	return ContainerState{}, unconfigured()
}

var _ Adapter = (*Fake)(nil)
