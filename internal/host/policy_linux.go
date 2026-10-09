//go:build linux

package host

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ShaulLavo/brine/internal/policy"
)

const PolicyPath = "/etc/ssh/brine/operator-policy.toml"
const RequesterPath = "/etc/ssh/brine/requester"

type DiskPolicy struct{ Path string }

func (p DiskPolicy) Load(ctx context.Context) (policy.Policy, error) {
	path := p.Path
	if path == "" {
		path = PolicyPath
	}
	raw, err := trustedRead(ctx, path, 1<<20)
	if err != nil {
		return policy.Policy{}, err
	}
	return productionPolicy(raw)
}
func productionPolicy(raw []byte) (policy.Policy, error) {
	pol, err := policy.Parse(raw)
	if err == nil && pol.CaddyPort() != 443 {
		return policy.Policy{}, errors.New("host: explicit Caddy HTTPS listener port 443 required")
	}
	return pol, err
}

func trustedRead(ctx context.Context, path string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("host: invalid trusted path")
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil {
			return nil, err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || st.Uid != 0 || info.Mode().Perm()&0022 != 0 {
			return nil, errors.New("host: untrusted policy parent")
		}
		if dir == "/" {
			break
		}
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Uid != 0 || st.Nlink != 1 || info.Mode().Perm()&0022 != 0 || info.Size() > limit {
		return nil, errors.New("host: untrusted policy file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("host: trusted file read failed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return raw, nil
}
