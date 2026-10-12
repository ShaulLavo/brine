//go:build linux

package host

import "context"

func (h archiveHost) stoppedOrAbsentService(ctx context.Context, name string) (bool, error) {
	stopped, err := h.serviceStopped(ctx, name, false)
	if err != nil || stopped {
		return stopped, err
	}
	return h.serviceStopped(ctx, name, true)
}
