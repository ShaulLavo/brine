package inventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/target"
)

const LitestreamPath = "/opt/brine/litestream/0.5.17/litestream"

type Litestream struct {
	Path           string `json:"path"`
	Version        string `json:"version"`
	ExecutableHash string `json:"executable_hash"`
}

// The injected reader must verify protected parents, file ownership, mode and type.
type LitestreamCollector struct {
	Runner         localexec.StdoutRunner
	HashExecutable func(context.Context, string) (string, error)
}

func (c LitestreamCollector) Collect(ctx context.Context) target.Observation[Litestream] {
	if c.Runner == nil {
		return unknown[Litestream]()
	}
	hash := c.HashExecutable
	if hash == nil {
		hash = protectedExecutableHash
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	before, err := hash(ctx, LitestreamPath)
	if errors.Is(err, os.ErrNotExist) {
		return absent[Litestream]()
	}
	if err != nil {
		return unknown[Litestream]()
	}
	version, err := c.Runner.RunStdout(ctx, LitestreamPath, "version")
	if err != nil || len(version) > 128 || strings.TrimSpace(version) != "0.5.17" {
		return unknown[Litestream]()
	}
	after, err := hash(ctx, LitestreamPath)
	if err != nil || after != before || ctx.Err() != nil {
		return unknown[Litestream]()
	}
	return target.Known(Litestream{Path: LitestreamPath, Version: "0.5.17", ExecutableHash: before})
}

// The version-only snapshot projects from the same protected version/hash fact
// used by enrollment, never from a PATH lookup or an unchecked executable.
func (c Collector) litestreamVersion(ctx context.Context) target.Observation[string] {
	observation := (LitestreamCollector{Runner: c.Runner, HashExecutable: c.LitestreamHashExecutable}).Collect(ctx)
	if observation.Status == target.KnownStatus && observation.Value != nil {
		return target.Known(observation.Value.Version)
	}
	return target.Observation[string]{Status: observation.Status}
}

func protectedExecutableHash(ctx context.Context, path string) (string, error) {
	return filesystemCall(ctx, func(ctx context.Context) (string, error) {
		for p := filepath.Dir(path); ; p = filepath.Dir(p) {
			info, err := os.Lstat(p)
			if err != nil {
				return "", err
			}
			if !protectedTool(info, true) {
				return "", errors.New("Litestream parent not protected")
			}
			if p == "/" {
				break
			}
		}
		fd, err := openTool(path)
		if err != nil {
			return "", err
		}
		defer fd.Close()
		info, err := fd.Stat()
		if err != nil || !protectedTool(info, false) || info.Size() > 64<<20 {
			return "", errors.New("Litestream executable not protected")
		}
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(fd, 64<<20+1))
		if err != nil || n != info.Size() || n > 64<<20 || ctx.Err() != nil {
			return "", errors.New("Litestream executable unreadable")
		}
		return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
	})
}
